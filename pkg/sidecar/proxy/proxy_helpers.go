package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"syscall"
	"time"
)

// startHTTP starts the HTTP reverse proxy.
func (s *Server) startHTTP(ctx context.Context, cert *tls.Certificate) error {
	// Start SSRF protection validator
	if err := s.allowlistValidator.Start(ctx); err != nil {
		s.logger.Error(err, "Failed to start allowlist validator")
		return err
	}

	ln, err := net.Listen("tcp", ":"+s.port)
	if err != nil {
		s.logger.Error(err, "Failed to start")
		return err
	}
	s.addr = ln.Addr()

	server := &http.Server{
		Handler: s.handler,
		// No ReadTimeout/WriteTimeout for LLM inference - can take hours for large contexts
		IdleTimeout:       300 * time.Second, // 5 minutes for keep-alive connections
		ReadHeaderTimeout: 30 * time.Second,  // Reasonable for headers only
		MaxHeaderBytes:    1 << 20,           // 1 MB for headers is sufficient
	}

	// Create TLS certificates
	if cert != nil {
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{*cert},
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
		s.logger.Info("server TLS configured")
	}

	// Setup graceful termination (not strictly needed for sidecars)
	go func() {
		<-ctx.Done()
		s.logger.Info("shutting down")

		// Stop allowlist validator
		s.allowlistValidator.Stop()

		ctx, cancelFn := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancelFn()
		if err := server.Shutdown(ctx); err != nil {
			s.logger.Error(err, "failed to gracefully shutdown")
		}
	}()

	s.logger.Info("starting", "addr", s.addr.String())
	if cert != nil {
		if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
			s.logger.Error(err, "failed to start")
			return err
		}
	} else {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.Error(err, "failed to start")
			return err
		}
	}

	return nil
}

// Passthrough decoder handler
func (s *Server) createDecoderProxyHandler(decoderURL *url.URL, decoderInsecureSkipVerify bool) *httputil.ReverseProxy {
	decoderProxy := httputil.NewSingleHostReverseProxy(decoderURL)
	if decoderURL.Scheme == "https" {
		decoderProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: decoderInsecureSkipVerify,
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
		var writeError error
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			s.logger.Error(err, "failed to connect to vLLM decoder",
				"decoderURL", s.decoderURL.String())
			res.WriteHeader(http.StatusServiceUnavailable)
			_, writeError = res.Write(decoderServiceUnavailableResponseJSON)

		default:
			s.logger.Error(err, "http: proxy error",
				"decoderURL", s.decoderURL.String())
			writeError = errorBadGateway(err, res)
		}
		if writeError != nil {
			s.logger.Error(err, "failed to send error response to client")
		}
	}
	return decoderProxy
}
