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
	"net/url"

	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/connectors"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy/manager"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// ConnectorNIXLV2 enables the P/D NIXL v2 protocol
	ConnectorNIXLV2 = "nixlv2"

	// ConnectorSharedStorage enables (now deprecated) P/D Shared Storage protocol
	ConnectorSharedStorage = "shared-storage"

	// ConnectorSGLang enables SGLang P/D disaggregation protocol
	ConnectorSGLang = "sglang"

	// DefaultPoolGroup is the default pool group name
	DefaultPoolGroup = "inference.networking.k8s.io"
	// LegacyPoolGroup is the legacy pool group name
	LegacyPoolGroup = "inference.networking.x-k8s.io"
)

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

// Server is the reverse proxy server
type Server struct {
	logger             logr.Logger
	addr               net.Addr     // the proxy TCP address
	port               string       // the proxy TCP port
	decoderURL         *url.URL     // the local decoder URL
	handler            http.Handler // the handler function. either a Mux or a proxy
	allowlistValidator *AllowlistValidator

	protocolRunner connectors.ProtocolRunner // the handler for running the protocol

	proxyManager manager.ProxyManager

	prefillSamplerFn func(n int) int // allow test override

	config Config
}

// NewProxy creates a new routing reverse proxy
func NewProxy(port string, decodeURL *url.URL, config Config) *Server {
	cache, _ := lru.New[string, http.Handler](16) // nolint:all

	server := &Server{
		port:       port,
		decoderURL: decodeURL,
		proxyManager: manager.ProxyManager{
			PrefillerURLPrefix:          "http://",
			PrefillerInsecureSkipVerify: config.PrefillerInsecureSkipVerify,

			PrefillerProxies:    cache,
			DataParallelProxies: map[string]http.Handler{},
			ForwardDataParallel: true,
		},
		config:           config,
		prefillSamplerFn: rand.Intn,
	}

	server.setConnector()

	if config.PrefillerUseTLS {
		server.proxyManager.PrefillerURLPrefix = "https://"
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
		addr:               s.addr,
		port:               s.port,
		decoderURL:         s.decoderURL,
		handler:            s.handler,
		allowlistValidator: s.allowlistValidator,
		protocolRunner:     s.protocolRunner,
		proxyManager:       s.proxyManager,
	}
}

func (s *Server) setConnector() {
	switch s.config.Connector {
	case ConnectorSharedStorage:
		s.protocolRunner = connectors.NewDecodeFirstRunner(
			&connectors.DefaultRequestBuilderFactory{},
			ConnectorSharedStorage,
			&s.proxyManager,
		)
	case ConnectorSGLang:
		s.protocolRunner = connectors.NewSGLangProtocolRunner(&s.proxyManager)
	case ConnectorNIXLV2:
		fallthrough
	default:
		s.protocolRunner = connectors.NewDecodeFirstRunner(
			&connectors.NIXLV2RequestBuilderFactory{},
			ConnectorNIXLV2,
			&s.proxyManager,
		)
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

	s.proxyManager.DecoderProxy = s.createDecoderProxyHandler(s.decoderURL, s.config.DecoderInsecureSkipVerify)

	mux.Handle("/", s.proxyManager.DecoderProxy)

	return mux
}
