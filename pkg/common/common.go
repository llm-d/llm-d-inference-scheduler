// Package common contains items common to both the
// EPP/Inference-Scheduler and the Routing Sidecar
//
//revive:disable:var-naming
package common

const (
	// PrefillPodHeader is the header name used to indicate Prefill worker <ip:port>
	PrefillPodHeader = "x-prefiller-host-port"

	// DataParallelPodHeader is the header name used to indicate the worker <ip:port> for Data Parallel
	DataParallelPodHeader = "x-data-parallel-host-port"

	// EncoderHostsPortsHeader is the header name used to indicate Encoder workers <ip:port> list
	EncoderHostsPortsHeader = "x-encoder-hosts-ports"
)
