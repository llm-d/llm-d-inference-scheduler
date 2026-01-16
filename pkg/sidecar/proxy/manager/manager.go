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

// Package manager manages the proxy handlers for the prefillers and the decoder
package manager

import (
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
)

// ProxyManager manages the proxy handlers for the prefillers and the decoder
type ProxyManager struct {
	PrefillerURLPrefix          string
	PrefillerInsecureSkipVerify bool

	DecoderProxy        http.Handler                     // decoder proxy handler
	PrefillerProxies    *lru.Cache[string, http.Handler] // cached prefiller proxy handlers
	DataParallelProxies map[string]http.Handler          // Proxies to other vLLM servers
	ForwardDataParallel bool                             // Use special Data Parallel workaround
}

// PrefillerProxyHandler returns a prefiller proxy handler for the given host port
func (m *ProxyManager) PrefillerProxyHandler(hostPort string, logger logr.Logger) (http.Handler, error) {
	proxy, exists := m.PrefillerProxies.Get(hostPort)
	if exists {
		return proxy, nil
	}

	// Backward compatible behavior: trim `http:` prefix
	hostPort, _ = strings.CutPrefix(hostPort, "http://")

	u, err := url.Parse(m.PrefillerURLPrefix + hostPort)
	if err != nil {
		logger.Error(err, "failed to parse URL", "hostPort", hostPort)
		return nil, err
	}

	newProxy := httputil.NewSingleHostReverseProxy(u)
	if u.Scheme == "https" {
		newProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: m.PrefillerInsecureSkipVerify,
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
	m.PrefillerProxies.Add(hostPort, newProxy)

	return newProxy, nil
}
