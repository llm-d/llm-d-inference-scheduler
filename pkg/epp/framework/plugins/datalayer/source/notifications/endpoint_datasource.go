package notifications

import (
	"context"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

var (
	_ fwkdl.DataSource     = (*EndpointDataSource)(nil)
	_ fwkdl.EndpointSource = (*EndpointDataSource)(nil)
)

// EndpointNotificationSourceType is the plugin type identifier for endpoint notification sources.
const EndpointNotificationSourceType = "endpoint-notification-source"

// EndpointDataSource is an EndpointSource that passes endpoint lifecycle events
// through to registered EndpointExtractors without modification.
type EndpointDataSource struct {
	typedName fwkplugin.TypedName
}

// NewEndpointDataSource returns a new EndpointDataSource with the given plugin type and name.
func NewEndpointDataSource(pluginType, pluginName string) *EndpointDataSource {
	return &EndpointDataSource{
		typedName: fwkplugin.TypedName{Type: pluginType, Name: pluginName},
	}
}

// TypedName returns the plugin type and name.
func (s *EndpointDataSource) TypedName() fwkplugin.TypedName {
	return s.typedName
}

// NotifyEndpoint passes the event through unchanged for the Runtime to dispatch to extractors.
func (s *EndpointDataSource) NotifyEndpoint(_ context.Context, event fwkdl.EndpointEvent) (*fwkdl.EndpointEvent, error) {
	return &event, nil
}
