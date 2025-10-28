package proxy

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"golang.org/x/sync/errgroup"
)

// dataParallelHandler checks if Data Parallel handling is needed.
// Returns true if Data Parallel processing was needed
func (s *Server) dataParallelHandler(w http.ResponseWriter, r *http.Request) bool {
	dataParallelPodHostPort := r.Header.Get(common.DataParallelPodHeader)
	if dataParallelPodHostPort != "" {
		handler := s.dataParallelProxies[dataParallelPodHostPort]
		if handler != nil {
			s.logger.V(4).Info("Data parallel routing", "to", dataParallelPodHostPort)
			handler.ServeHTTP(w, r)
		} else {
			// Shouldn't happen, send to default server
			s.logger.V(4).Info("Didn't find the Data Parallel Proxy", "for", dataParallelPodHostPort)
			w.WriteHeader(http.StatusBadRequest)
		}
		return true
	}

	s.logger.V(4).Info("skip data parallel")
	return false
}

func (s *Server) startDataParallel(ctx context.Context, cert *tls.Certificate, grp *errgroup.Group) error {
	podIP := os.Getenv("POD_IP")
	basePort, err := strconv.Atoi(s.port)
	if err != nil {
		return err
	}
	baseDecoderPort, err := strconv.Atoi(s.decoderURL.Port())
	if err != nil {
		return err
	}

	s.dataParallelProxies[net.JoinHostPort(podIP, s.port)] = s.decoderProxy

	// Fill in map of proxies, thus avoiding locks
	for idx := range s.config.DataParallelSize - 1 {
		decoderPort := strconv.Itoa(baseDecoderPort + idx + 1)
		rankPort := strconv.Itoa(basePort + idx + 1)
		hostPort := net.JoinHostPort(podIP, rankPort)
		rankURL, err := url.Parse(s.decoderURL.Scheme + "://localhost:" + decoderPort)
		if err != nil {
			return err
		}
		handler := s.createDecoderProxyHandler(rankURL, s.config.DecoderInsecureSkipVerify)
		s.dataParallelProxies[hostPort] = handler
	}

	for idx := range s.config.DataParallelSize - 1 {
		grp.Go(func() error {
			rankPort := strconv.Itoa(basePort + idx + 1)
			hostPort := net.JoinHostPort(podIP, rankPort)
			handler := s.dataParallelProxies[hostPort]

			simpleProxy := NewSimpleProxy(rankPort, s.config.DecoderInsecureSkipVerify)

			return simpleProxy.Start(ctx, cert, s.allowlistValidator, handler)
		})
	}
	return nil
}
