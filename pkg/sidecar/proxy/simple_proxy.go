package proxy

import (
	"context"
	"crypto/tls"
	"net/http"

	"k8s.io/klog/v2"
)

// SimpleProxy is simple straight through proxy, mostly for /metrics
type SimpleProxy struct {
	BaseServer
	decoderInsecureSkipVerify bool
}

// NewSimpleProxy creates a new simple reverse proxy
func NewSimpleProxy(port string, decoderInsecureSkipVerify bool) *SimpleProxy {
	server := &SimpleProxy{
		BaseServer: BaseServer{
			port: port,
		},
		decoderInsecureSkipVerify: decoderInsecureSkipVerify,
	}

	return server
}

// Start the HTTP reverse proxy.
func (s *SimpleProxy) Start(ctx context.Context, cert *tls.Certificate, allowlistValidator *AllowlistValidator, handler http.Handler) error {
	logger := klog.FromContext(ctx).WithName("simple proxy server on " + s.port)
	s.logger = logger

	s.allowlistValidator = allowlistValidator

	s.handler = handler

	return s.BaseStart(ctx, cert)
}
