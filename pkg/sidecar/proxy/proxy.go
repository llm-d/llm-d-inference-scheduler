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
	requestFieldMaxOutputTokens     = "max_output_tokens" // Used by Responses/Conversations API
	requestFieldDoRemotePrefill     = "do_remote_prefill"
	requestFieldDoRemoteDecode      = "do_remote_decode"
	requestFieldRemoteBlockIDs      = "remote_block_ids"
	requestFieldRemoteEngineID      = "remote_engine_id"
	requestFieldRemoteHost          = "remote_host"
	requestFieldRemotePort          = "remote_port"
	requestFieldStream              = "stream"
	requestFieldStreamOptions       = "stream_options"

	// SGLang bootstrap fields
	requestFieldBootstrapHost = "bootstrap_host"
	requestFieldBootstrapPort = "bootstrap_port"
	requestFieldBootstrapRoom = "bootstrap_room"

	// ConnectorNIXLV2 enables the P/D NIXL v2 protocol
	ConnectorNIXLV2 = "nixlv2"

	// ConnectorLMCache enables (now deprecated) P/D LMCache protocol
	ConnectorLMCache = "lmcache"

	// ConnectorSGLang enables SGLang P/D disaggregation protocol
	ConnectorSGLang = "sglang"

	// DefaultPoolGroup is the default pool group name
	DefaultPoolGroup = "inference.networking.k8s.io"
	// LegacyPoolGroup is the legacy pool group name
	LegacyPoolGroup = "inference.networking.x-k8s.io"
)

// APIType represents the type of OpenAI API being used
type APIType int

const (
	// APITypeChatCompletions is the Chat Completions API (/v1/chat/completions, /v1/completions)
	APITypeChatCompletions APIType = iota
	// APITypeResponses is the Responses API (/v1/responses)
	APITypeResponses
	// APITypeConversations is the Conversations API (/v1/conversations)
	APITypeConversations
)

// JSON request field names used for token limits in prefill/decode staging.
// Do not mutate these slices.
var (
	chatCompletionTokenLimitFields = []string{requestFieldMaxTokens, requestFieldMaxCompletionTokens}
	responsesStyleTokenLimitFields = []string{requestFieldMaxOutputTokens}
)

// tokenLimitFieldsForAPIType returns token limit field names for the given API.
// Returned slices are shared package-level vars; callers must not mutate them.
func tokenLimitFieldsForAPIType(api APIType) []string {
	switch api {
	case APITypeResponses, APITypeConversations:
		return responsesStyleTokenLimitFields
	default:
		return chatCompletionTokenLimitFields
	}
}

// Config represents the proxy server configuration
type Config struct {
	// Connector is the name of the P/D protocol the proxy must follow.
	Connector string

	// PrefillerUseTLS indicates whether to use TLS when sending requests to prefillers.
	PrefillerUseTLS bool

	// PrefillerInsecureSkipVerify configure the proxy to skip TLS verification for requests to prefiller.
	PrefillerInsecureSkipVerify bool

	// DecoderInsecureSkipVerify configure the proxy to skip TLS verification for requests to decoder.
	DecoderInsecureSkipVerify bool

	// DataParallelSize is the value passed to the vLLM server's --DATA_PARALLEL-SIZE command line argument
	DataParallelSize int

	// EnablePrefillerSampling configures the proxy to randomly choose from the set
	// of provided prefill hosts instead of always using the first one.
	EnablePrefillerSampling bool
}

// protocolRunner runs the configured P/D connector. The final argument lists JSON keys
// for token limits (NIXL v2 and LMCache); SGLang ignores it.
type protocolRunner func(http.ResponseWriter, *http.Request, string, []string)

// Server is the reverse proxy server
type Server struct {
	logger               logr.Logger
	addr                 net.Addr     // the proxy TCP address
	port                 string       // the proxy TCP port
	decoderURL           *url.URL     // the local decoder URL
	handler              http.Handler // the handler function. either a Mux or a proxy
	allowlistValidator   *AllowlistValidator
	runConnectorProtocol protocolRunner // the handler for running the P/D protocol
	prefillerURLPrefix   string

	decoderProxy        http.Handler                     // decoder proxy handler
	prefillerProxies    *lru.Cache[string, http.Handler] // cached prefiller proxy handlers
	dataParallelProxies map[string]http.Handler          // Proxies to other vLLM servers
	forwardDataParallel bool                             // Use special Data Parallel work around

	prefillSamplerFn func(n int) int // allow test override

	config Config
}

// NewProxy creates a new routing reverse proxy
func NewProxy(port string, decodeURL *url.URL, config Config) *Server {
	cache, _ := lru.New[string, http.Handler](16) // nolint:all

	server := &Server{
		port:                port,
		decoderURL:          decodeURL,
		prefillerProxies:    cache,
		prefillerURLPrefix:  "http://",
		config:              config,
		dataParallelProxies: map[string]http.Handler{},
		forwardDataParallel: true,
		prefillSamplerFn:    rand.Intn,
	}

	server.setConnector()

	if config.PrefillerUseTLS {
		server.prefillerURLPrefix = "https://"
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
		addr:                 s.addr,
		port:                 s.port,
		decoderURL:           s.decoderURL,
		handler:              s.handler,
		allowlistValidator:   s.allowlistValidator,
		runConnectorProtocol: s.runConnectorProtocol,
		decoderProxy:         s.decoderProxy,
		prefillerURLPrefix:   s.prefillerURLPrefix,
		prefillerProxies:     s.prefillerProxies,
		dataParallelProxies:  s.dataParallelProxies,
		forwardDataParallel:  s.forwardDataParallel,
	}
}

func (s *Server) setConnector() {

	switch s.config.Connector {
	case ConnectorLMCache:
		s.runConnectorProtocol = s.runLMCacheProtocol
	case ConnectorSGLang:
		s.runConnectorProtocol = func(w http.ResponseWriter, r *http.Request, host string, _ []string) {
			s.runSGLangProtocol(w, r, host)
		}
	case ConnectorNIXLV2:
		fallthrough
	default:
		s.runConnectorProtocol = s.runNIXLProtocolV2
	}
}

func (s *Server) createRoutes() *http.ServeMux {
	// Configure handlers
	mux := http.NewServeMux()

	// Intercept chat requests
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST "+ChatCompletionsPath, s.disaggregatedPrefillHandler(APITypeChatCompletions, "skip disaggregated prefill"))
	mux.HandleFunc("POST "+CompletionsPath, s.disaggregatedPrefillHandler(APITypeChatCompletions, "skip disaggregated prefill"))
	mux.HandleFunc("POST "+ResponsesPath, s.disaggregatedPrefillHandler(APITypeResponses, "skip disaggregated prefill for responses API"))
	mux.HandleFunc("POST "+ConversationsPath, s.disaggregatedPrefillHandler(APITypeConversations, "skip disaggregated prefill for conversations API"))

	s.decoderProxy = s.createDecoderProxyHandler(s.decoderURL, s.config.DecoderInsecureSkipVerify)

	mux.Handle("/", s.decoderProxy)

	return mux
}

func (s *Server) prefillerProxyHandler(hostPort string) (http.Handler, error) {
	proxy, exists := s.prefillerProxies.Get(hostPort)
	if exists {
		return proxy, nil
	}

	// Backward compatible behavior: trim `http:` prefix
	hostPort, _ = strings.CutPrefix(hostPort, "http://")

	u, err := url.Parse(s.prefillerURLPrefix + hostPort)
	if err != nil {
		s.logger.Error(err, "failed to parse URL", "hostPort", hostPort)
		return nil, err
	}

	newProxy := httputil.NewSingleHostReverseProxy(u)
	if u.Scheme == "https" {
		newProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: s.config.PrefillerInsecureSkipVerify,
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
	s.prefillerProxies.Add(hostPort, newProxy)

	return newProxy, nil
}
