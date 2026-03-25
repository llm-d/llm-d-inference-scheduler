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
	"encoding/json"
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
	schemeHTTPS = "https"

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

	// KVConnectorNIXLV2 enables the P/D KV NIXL v2 protocol
	KVConnectorNIXLV2 = "nixlv2"

	// KVConnectorSharedStorage enables the P/D KV Shared Storage protocol
	KVConnectorSharedStorage = "shared-storage"

	// KVConnectorSGLang enables SGLang the P/D KV disaggregation protocol
	KVConnectorSGLang = "sglang"

	// ECExampleConnector enables the Encoder disaggregation protocol (E/PD, E/P/D)
	ECExampleConnector = "ec-example"

	// DefaultPoolGroup is the default pool group name
	DefaultPoolGroup = "inference.networking.k8s.io"
	// LegacyPoolGroup is the legacy pool group name
	LegacyPoolGroup = "inference.networking.x-k8s.io"
)

// Config represents the complete runtime configuration for the proxy server.
type Config struct {
	// Port is the port the sidecar is listening on.
	Port string
	// DecoderURL is the URL of the local decoder (vLLM) instance.
	DecoderURL *url.URL

	// KVConnector is the name of the KV protocol between prefiller and decoder.
	KVConnector string
	// ECConnector is the name of the EC protocol between encoder and prefiller (for EPD mode).
	// If empty, encoder stage is skipped.
	ECConnector string
	// DataParallelSize is the value passed to the vLLM server's --DATA_PARALLEL-SIZE argument.
	DataParallelSize int
	// EnablePrefillerSampling configures the proxy to randomly choose from the set
	// of provided prefill hosts instead of always using the first one.
	EnablePrefillerSampling bool

	// UseTLSForPrefiller indicates whether to use TLS when sending requests to prefillers.
	UseTLSForPrefiller bool
	// UseTLSForDecoder indicates whether to use TLS when sending requests to the decoder.
	UseTLSForDecoder bool
	// UseTLSForEncoder indicates whether to use TLS when sending requests to encoders.
	UseTLSForEncoder bool
	// InsecureSkipVerifyForPrefiller configures the proxy to skip TLS verification for requests to the prefiller.
	InsecureSkipVerifyForPrefiller bool
	// InsecureSkipVerifyForEncoder configures the proxy to skip TLS verification for requests to the encoder.
	InsecureSkipVerifyForEncoder bool
	// InsecureSkipVerifyForDecoder configures the proxy to skip TLS verification for requests to the decoder.
	InsecureSkipVerifyForDecoder bool

	// SecureServing enables TLS for the sidecar server itself.
	SecureServing bool
	// CertPath is the path to TLS certificates for the sidecar server.
	CertPath string

	// EnableSSRFProtection enables SSRF protection using InferencePool allowlisting.
	EnableSSRFProtection bool
	// InferencePoolNamespace is the Kubernetes namespace of the InferencePool to watch.
	InferencePoolNamespace string
	// InferencePoolName is the name of the InferencePool to watch.
	InferencePoolName string
	// PoolGroup is the API group of the InferencePool resource.
	PoolGroup string
}

// MarshalJSON implements json.Marshaler for Config.
// It overrides the default marshaling of DecoderURL (*url.URL) to serialize it as a string.
func (c Config) MarshalJSON() ([]byte, error) {
	// alias avoids infinite recursion when calling json.Marshal below
	type alias Config
	decoderURL := ""
	if c.DecoderURL != nil {
		decoderURL = c.DecoderURL.String()
	}
	return json.Marshal(struct {
		alias
		DecoderURL string
	}{
		alias:      alias(c),
		DecoderURL: decoderURL,
	})
}

// String returns a JSON representation of Config for logging and debugging.
// It implements fmt.Stringer.
func (c Config) String() string {
	b, _ := json.Marshal(c)
	return string(b)
}

type protocolRunner func(http.ResponseWriter, *http.Request, string)
type epdProtocolRunner func(http.ResponseWriter, *http.Request, string, []string)

// Server is the reverse proxy server
type Server struct {
	logger                  logr.Logger
	addr                    net.Addr      // the proxy TCP address
	readyCh                 chan struct{} // closed once addr is set and server is listening
	handler                 http.Handler  // the handler function. either a Mux or a proxy
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

// NewProxy creates a new routing reverse proxy from the given Config.
func NewProxy(config Config) *Server {
	prefillerCache, _ := lru.New[string, http.Handler](16) // nolint:all
	encoderCache, _ := lru.New[string, http.Handler](16)   // nolint:all

	server := &Server{
		readyCh:             make(chan struct{}),
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
	if config.UseTLSForPrefiller {
		server.prefillerURLPrefix = "https://"
	}

	if config.ECConnector != "" {
		server.setECConnector()
		if config.UseTLSForEncoder {
			server.encoderURLPrefix = "https://"
		}
	}

	return server
}

// Start the HTTP reverse proxy.
// allowlistValidator is constructed from s.config on first call; inject an alternative before calling Start to override.
func (s *Server) Start(ctx context.Context) error {
	s.logger = log.FromContext(ctx).WithName("proxy server on port " + s.config.Port)

	if s.allowlistValidator == nil {
		var err error
		s.allowlistValidator, err = NewAllowlistValidator(
			s.config.EnableSSRFProtection,
			s.config.PoolGroup,
			s.config.InferencePoolNamespace,
			s.config.InferencePoolName,
		)
		if err != nil {
			return err
		}
	}

	// Configure handlers
	s.handler = s.createRoutes()

	grp, ctx := errgroup.WithContext(ctx)
	if err := s.startDataParallel(ctx, grp); err != nil {
		return err
	}

	grp.Go(func() error {
		return s.startHTTP(ctx)
	})

	return grp.Wait()
}

// Clone returns a clone of the current Server struct.
// Note: decoderURL and decoderProxy are intentionally not copied — callers (e.g. startDataParallel)
// always set them explicitly after cloning.
func (s *Server) Clone() *Server {
	return &Server{
		addr:                    s.addr,
		readyCh:                 make(chan struct{}),
		handler:                 s.handler,
		allowlistValidator:      s.allowlistValidator,
		runPDConnectorProtocol:  s.runPDConnectorProtocol,
		runEPDConnectorProtocol: s.runEPDConnectorProtocol,
		prefillerURLPrefix:      s.prefillerURLPrefix,
		encoderURLPrefix:        s.encoderURLPrefix,
		prefillerProxies:        s.prefillerProxies,
		encoderProxies:          s.encoderProxies,
		dataParallelProxies:     s.dataParallelProxies,
		forwardDataParallel:     s.forwardDataParallel,
		prefillSamplerFn:        s.prefillSamplerFn,
		config:                  s.config,
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

	s.decoderProxy = s.createDecoderProxyHandler(s.config.DecoderURL, s.config.InsecureSkipVerifyForDecoder)

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
	if u.Scheme == schemeHTTPS {
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
		s.config.InsecureSkipVerifyForPrefiller,
	)
}

func (s *Server) encoderProxyHandler(hostPort string) (http.Handler, error) {
	return s.createProxyHandler(
		hostPort,
		s.encoderProxies,
		s.encoderURLPrefix,
		s.config.InsecureSkipVerifyForEncoder,
	)
}
