/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"context"
	"crypto/tls"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	requestHeaderRequestID = "x-request-id"

	requestFieldKVTransferParams    = "kv_transfer_params"
	requestFieldMaxTokens           = "max_tokens"
	requestFieldMaxCompletionTokens = "max_completion_tokens"
	requestFieldDoRemotePrefill     = "do_remote_prefill"
	requestFieldDoRemoteDecode      = "do_remote_decode"
	requestFieldRemoteBlockIDs      = "remote_block_ids"
	requestFieldRemoteEngineID      = "remote_engine_id"
	requestFieldRemoteHost          = "remote_host"
	requestFieldRemotePort          = "remote_port"
	requestFieldStream              = "stream"
	requestFieldStreamOptions       = "stream_options"
	requestFieldCacheHitThreshold   = "cache_hit_threshold"

	responseFieldChoices      = "choices"
	responseFieldFinishReason = "finish_reason"

	finishReasonCacheThreshold = "cache_threshold"

	// SGLang bootstrap fields
	requestFieldBootstrapHost = "bootstrap_host"
	requestFieldBootstrapPort = "bootstrap_port"
	requestFieldBootstrapRoom = "bootstrap_room"

	// KVConnectorNIXLV2 enables the P/D NIXL v2 protocol
	KVConnectorNIXLV2 = "nixlv2"

	// KVConnectorSharedStorage enables (now deprecated) P/D Shared Storage protocol
	KVConnectorSharedStorage = "shared-storage"

	// KVConnectorSGLang enables SGLang P/D disaggregation protocol
	KVConnectorSGLang = "sglang"

	// ECExampleConnector enables the Encoder-Prefiller-Decoder disaggregation protocol
	ECExampleConnector = "ec-example"

	// DefaultPoolGroup is the default pool group name
	DefaultPoolGroup = "inference.networking.k8s.io"
	// LegacyPoolGroup is the legacy pool group name
	LegacyPoolGroup = "inference.networking.x-k8s.io"
)

// Config represents the proxy server configuration
type Config struct {
	// KVConnector is the name of the KV protocol between Prefiller and Decoder.
	KVConnector string

	// ECConnector is the name of the EC protocol between Encoder and Prefiller (for EPD mode).
	// If empty, encoder stage is skipped.
	ECConnector string

	// PrefillerUseTLS indicates whether to use TLS when sending requests to prefillers.
	PrefillerUseTLS bool

	// EncoderUseTLS indicates whether to use TLS when sending requests to encoders.
	EncoderUseTLS bool

	// PrefillerInsecureSkipVerify configure the proxy to skip TLS verification for requests to prefiller.
	PrefillerInsecureSkipVerify bool

	// EncoderInsecureSkipVerify configure the proxy to skip TLS verification for requests to encoder.
	EncoderInsecureSkipVerify bool

	// DecoderInsecureSkipVerify configure the proxy to skip TLS verification for requests to decoder.
	DecoderInsecureSkipVerify bool

	// DataParallelSize is the value passed to the vLLM server's --DATA_PARALLEL-SIZE command line argument
	DataParallelSize int

	// EnablePrefillerSampling configures the proxy to randomly choose from the set
	// of provided prefill hosts instead of always using the first one.
	EnablePrefillerSampling bool
}

type protocolRunner func(http.ResponseWriter, *http.Request, string)
type epdProtocolRunner func(http.ResponseWriter, *http.Request, string, []string)

// Server is the reverse proxy server
type Server struct {
	logger                  logr.Logger
	addr                    net.Addr     // the proxy TCP address
	port                    string       // the proxy TCP port
	decoderURL              *url.URL     // the local decoder URL
	handler                 http.Handler // the handler function. either a Mux or a proxy
	allowlistValidator      *AllowlistValidator
	runPDConnectorProtocol  protocolRunner    // the handler for running the Prefiller-Decoder protocol
	runEPDConnectorProtocol epdProtocolRunner // the handler for running the Encoder-Prefiller-Decoder protocol
	prefillerURLPrefix      string
	encoderURLPrefix        string

	decoderProxy        http.Handler                     // decoder proxy handler
	prefillerProxies    *lru.Cache[string, http.Handler] // cached prefiller proxy handlers
	encoderProxies      *lru.Cache[string, http.Handler] // cached encoder proxy handlers
	dataParallelProxies map[string]http.Handler          // Proxies to other vLLM servers
	forwardDataParallel bool                             // Use special Data Parallel work around

	prefillSamplerFn func(n int) int // allow test override

	config Config
}

// NewProxy creates a new routing reverse proxy
func NewProxy(port string, decodeURL *url.URL, config Config) *Server {
	prefillerCache, _ := lru.New[string, http.Handler](16) // nolint:all
	encoderCache, _ := lru.New[string, http.Handler](16)   // nolint:all

	server := &Server{
		port:                port,
		decoderURL:          decodeURL,
		prefillerProxies:    prefillerCache,
		encoderProxies:      encoderCache,
		prefillerURLPrefix:  "http://",
		encoderURLPrefix:    "http://",
		config:              config,
		dataParallelProxies: map[string]http.Handler{},
		forwardDataParallel: true,
		prefillSamplerFn:    rand.Intn,
	}

	server.setKVConnector()
	if config.PrefillerUseTLS {
		server.prefillerURLPrefix = "https://"
	}

	if config.ECConnector != "" {
		server.setECConnector()
		if config.EncoderUseTLS {
			server.encoderURLPrefix = "https://"
		}
	}

	return server
}

// Start the HTTP reverse proxy.
func (s *Server) Start(ctx context.Context, cert *tls.Certificate, allowlistValidator *AllowlistValidator) error {
	s.logger = log.FromContext(ctx).WithName("proxy server on port " + s.port)

	s.allowlistValidator = allowlistValidator

	// Configure handlers
	s.handler = s.createRoutes()

	grp, ctx := errgroup.WithContext(ctx)
	if err := s.startDataParallel(ctx, cert, grp); err != nil {
		return err
	}

	grp.Go(func() error {
		return s.startHTTP(ctx, cert)
	})

	return grp.Wait()
}

// Clone returns a clone of the current Server struct
func (s *Server) Clone() *Server {
	return &Server{
		addr:                    s.addr,
		port:                    s.port,
		decoderURL:              s.decoderURL,
		handler:                 s.handler,
		allowlistValidator:      s.allowlistValidator,
		runPDConnectorProtocol:  s.runPDConnectorProtocol,
		runEPDConnectorProtocol: s.runEPDConnectorProtocol,
		decoderProxy:            s.decoderProxy,
		prefillerURLPrefix:      s.prefillerURLPrefix,
		encoderURLPrefix:        s.encoderURLPrefix,
		prefillerProxies:        s.prefillerProxies,
		encoderProxies:          s.encoderProxies,
		dataParallelProxies:     s.dataParallelProxies,
		forwardDataParallel:     s.forwardDataParallel,
	}
}

func (s *Server) setKVConnector() {

	switch s.config.KVConnector {
	case KVConnectorSharedStorage:
		s.runPDConnectorProtocol = s.runSharedStorageProtocol
	case KVConnectorSGLang:
		s.runPDConnectorProtocol = s.runSGLangProtocol
	case KVConnectorNIXLV2:
		fallthrough
	default:
		s.runPDConnectorProtocol = s.runNIXLProtocolV2
	}
}

func (s *Server) setECConnector() {
	ecConnector := s.config.ECConnector

	if ecConnector == "" {
		// No encoder connector specified, encoder stage will be skipped
		return
	}

	switch ecConnector {
	case ECExampleConnector:
		s.runEPDConnectorProtocol = s.runEPDProtocol
	default:
		// Unknown EC connector value, skip encoder stage
		return
	}
}

func (s *Server) createRoutes() *http.ServeMux {
	// Configure handlers
	mux := http.NewServeMux()

	// Intercept chat requests
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST "+ChatCompletionsPath, s.chatCompletionsHandler) // /v1/chat/completions (openai)
	mux.HandleFunc("POST "+CompletionsPath, s.chatCompletionsHandler)     // /v1/completions (legacy)

	s.decoderProxy = s.createDecoderProxyHandler(s.decoderURL, s.config.DecoderInsecureSkipVerify)

	mux.Handle("/", s.decoderProxy)

	return mux
}

// createProxyHandler creates a reverse proxy handler for the given host:port.
// It uses the provided cache, URL prefix, and TLS settings.
func (s *Server) createProxyHandler(
	hostPort string,
	cache *lru.Cache[string, http.Handler],
	urlPrefix string,
	insecureSkipVerify bool,
) (http.Handler, error) {
	// Check cache first
	proxy, exists := cache.Get(hostPort)
	if exists {
		return proxy, nil
	}

	// Backward compatible behavior: trim `http:` prefix
	hostPort, _ = strings.CutPrefix(hostPort, "http://")

	u, err := url.Parse(urlPrefix + hostPort)
	if err != nil {
		s.logger.Error(err, "failed to parse URL", "hostPort", hostPort)
		return nil, err
	}

	newProxy := httputil.NewSingleHostReverseProxy(u)
	if u.Scheme == "https" {
		newProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecureSkipVerify,
				MinVersion:         tls.VersionTLS12,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
					tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				},
			},
		}
	}
	cache.Add(hostPort, newProxy)

	return newProxy, nil
}

func (s *Server) prefillerProxyHandler(hostPort string) (http.Handler, error) {
	return s.createProxyHandler(
		hostPort,
		s.prefillerProxies,
		s.prefillerURLPrefix,
		s.config.PrefillerInsecureSkipVerify,
	)
}

func (s *Server) encoderProxyHandler(hostPort string) (http.Handler, error) {
	return s.createProxyHandler(
		hostPort,
		s.encoderProxies,
		s.encoderURLPrefix,
		s.config.EncoderInsecureSkipVerify,
	)
}
