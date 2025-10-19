// Package common contains items common to both the
// EPP/Inference-Scheduler and the Routing Sidecar
package common

const (
	// prefillPodHeader is the header name used to indicate Prefill worker <ip:port>
	PrefillPodHeader = "x-prefiller-host-port"
)
