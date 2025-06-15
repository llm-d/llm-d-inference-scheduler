// Package http contains all types of external http plugins
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/valyala/fasthttp"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// Filter implementation of the external http filter
type Filter struct {
	Plugin
}

var _ plugins.Filter = &Filter{} // validate interface conformance

// NewFilter creates a new instance of external http filter based on the given parameters
func NewFilter(_ context.Context, name string, url string) plugins.Filter {
	return &Filter{Plugin{name: name, url: url}}
}

// Filter filters the given list of pods
func (f *Filter) Filter(schedContext *types.SchedulingContext, pods []types.Pod) []types.Pod {
	logger := log.FromContext(schedContext).WithName(f.Name())

	// Create filter http request payload based on the given data
	filterPayload := newFilterPayload(schedContext, pods)
	payload, err := json.Marshal(filterPayload)

	if err != nil {
		logger.Error(err, "Failed to marshal scheduling context, filter will be skipped")
		return pods
	}

	// Create a new fasthttp request and response
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(path.Join(f.url, "filter"))
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBody(payload)

	// Execute the request
	client := &fasthttp.Client{}
	if err := client.Do(req, resp); err != nil {
		logger.Error(err, "request failed")
		return pods
	}

	// Optionally check status code
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Error(nil, fmt.Sprintf("bad response status: %d, body: %s", resp.StatusCode(), resp.Body()))
		return pods
	}

	var filteredPodNames []namespacedName

	// filter plugin response is an array of pod full names
	if err := json.Unmarshal(resp.Body(), &filteredPodNames); err != nil {
		logger.Error(err, "external filter's response body unmarshal failed", "name", f.Name(), "resp body", resp.Body())
		return pods
	}

	// filter list of given pods based on the returned list of pods
	podsNamesSet := map[string]bool{}

	for _, nn := range filteredPodNames {
		podsNamesSet[namespacedNameToString(nn.Name, nn.Namespace)] = true
	}

	filteredPods := make([]types.Pod, 0)

	for _, p := range pods {
		nn := p.GetPod().NamespacedName
		if _, exists := podsNamesSet[namespacedNameToString(nn.Name, nn.Namespace)]; exists {
			filteredPods = append(filteredPods, p)
		}
	}

	return filteredPods
}
