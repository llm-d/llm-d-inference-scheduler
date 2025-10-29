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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/klog/v2"
)

const (
	requestHeaderPrefillURL = "x-prefiller-url"
	requestHeaderRequestID  = "x-request-id"

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

	// ConnectorNIXLV2 enables the P/D NIXL v2 protocol
	ConnectorNIXLV2 = "nixlv2"

	// ConnectorLMCache enables (now deprecated) P/D LMCache protocol
	ConnectorLMCache = "lmcache"
)

// Config represents the proxy server configuration
type Config struct {
	// Connector is the name of the P/D protocol the proxy must follow.
	Connector string

	// PrefillerUseTLS indicates whether to use TLS when sending requests to prefillers.
	PrefillerUseTLS bool

	// SecureProxy enables secure proxy when true
	SecureProxy bool

	// CertPath is the location of the TLS certificates
	CertPath string

	// PrefillerInsecureSkipVerify configure the proxy to skip TLS verification for requests to prefiller.
	PrefillerInsecureSkipVerify bool

	// DecoderInsecureSkipVerify configure the proxy to skip TLS verification for requests to decoder.
	DecoderInsecureSkipVerify bool

	// EnableSSRFProtection enables SSRF protection.
	EnableSSRFProtection bool

	// InferencePoolNamespace InferencePool object namespace.
	InferencePoolNamespace string

	// InferencePoolName InferencePool object name.
	InferencePoolName string
}

type protocolRunner func(http.ResponseWriter, *http.Request, string)

// Server is the reverse proxy server
type Server struct {
	logger               logr.Logger
	addr                 net.Addr       // the proxy TCP address
	port                 string         // the proxy TCP port
	decoderURL           *url.URL       // the local decoder URL
	decoderProxy         http.Handler   // decoder proxy handler
	runConnectorProtocol protocolRunner // the handler for running the protocol
	prefillerURLPrefix   string
	allowlistValidator   *AllowlistValidator // SSRF protection validator

	prefillerProxies *lru.Cache[string, http.Handler] // cached prefiller proxy handlers

	config Config
}

// NewProxy creates a new routing reverse proxy
func NewProxy(port string, decodeURL *url.URL, config Config) (*Server, error) {
	cache, _ := lru.New[string, http.Handler](16) // nolint:all

	// Create SSRF protection validator
	validator, err := NewAllowlistValidator(config.EnableSSRFProtection, config.InferencePoolNamespace, config.InferencePoolName)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSRF protection validator: %w", err)
	}

	server := &Server{
		port:               port,
		decoderURL:         decodeURL,
		prefillerProxies:   cache,
		prefillerURLPrefix: "http://",
		allowlistValidator: validator,
		config:             config,
	}
	switch config.Connector {
	case ConnectorLMCache:
		server.runConnectorProtocol = server.runLMCacheProtocol
	case ConnectorNIXLV2:
		fallthrough
	default:
		server.runConnectorProtocol = server.runNIXLProtocolV2
	}

	if config.PrefillerUseTLS {
		server.prefillerURLPrefix = "https://"
	}

	return server, nil
}

// Start the HTTP reverse proxy.
func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName("proxy server")
	s.logger = logger

	// Start SSRF protection validator
	if err := s.allowlistValidator.Start(ctx); err != nil {
		logger.Error(err, "Failed to start allowlist validator")
		return err
	}

	ln, err := net.Listen("tcp", ":"+s.port)
	if err != nil {
		logger.Error(err, "Failed to start")
		return err
	}
	s.addr = ln.Addr()

	// Configure handlers
	mux := s.createRoutes()

	server := &http.Server{
		Handler: mux,
		// No ReadTimeout/WriteTimeout for LLM inference - can take hours for large contexts
		IdleTimeout:       300 * time.Second, // 5 minutes for keep-alive connections
		ReadHeaderTimeout: 30 * time.Second,  // Reasonable for headers only
		MaxHeaderBytes:    1 << 20,           // 1 MB for headers is sufficient
	}

	// Create TLS certificates
	if s.config.SecureProxy {
		var cert tls.Certificate
		if s.config.CertPath != "" {
			cert, err = tls.LoadX509KeyPair(s.config.CertPath+"/tls.crt", s.config.CertPath+"/tls.key")
		} else {
			cert, err = CreateSelfSignedTLSCertificate()
		}
		if err != nil {
			logger.Error(err, "failed to create TLS certificate")
			return err
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			},
		}
		logger.Info("server TLS configured")
	}

	// Setup graceful termination (not strictly needed for sidecars)
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")

		// Stop allowlist validator
		s.allowlistValidator.Stop()

		ctx, cancelFn := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelFn()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error(err, "failed to gracefully shutdown")
		}
	}()

	logger.Info("starting", "addr", s.addr.String())
	if s.config.SecureProxy {
		if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "failed to start")
			return err
		}
	} else {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "failed to start")
			return err
		}
	}

	return nil
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

	// Passthrough decoder handler
	decoderProxy := httputil.NewSingleHostReverseProxy(s.decoderURL)
	if s.decoderURL.Scheme == "https" {
		decoderProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: s.config.DecoderInsecureSkipVerify,
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
	decoderProxy.ErrorHandler = func(res http.ResponseWriter, _ *http.Request, err error) {

		// Log errors from the decoder proxy
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			s.logger.Error(err, "waiting for vLLM to be ready")
		default:
			s.logger.Error(err, "http: proxy error")
		}
		res.WriteHeader(http.StatusBadGateway)
	}
	s.decoderProxy = decoderProxy
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
