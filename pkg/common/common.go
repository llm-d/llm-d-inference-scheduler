// Package common contains items common to both the
// EPP/Inference-Scheduler and the Routing Sidecar
//
//revive:disable:var-naming
package common

import "net/url"

const (
	// PrefillPodHeader is the header name used to indicate Prefill worker <ip:port>
	PrefillPodHeader = "x-prefiller-host-port"

	// DataParallelPodHeader is the header name used to indicate the worker <ip:port> for Data Parallel
	DataParallelPodHeader = "x-data-parallel-host-port"
)

// StripScheme removes the scheme from an endpoint URL, returning host:port.
// This is useful for gRPC clients that expect host:port format only.
func StripScheme(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint // not a valid URL, return as-is
	}
	return u.Host
}
