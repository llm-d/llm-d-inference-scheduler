package plugins

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	attrconcurrency "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/concurrency"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/inflightload"
)

func TestRegisterAllPluginsRegistersInflightLoadDefaultProducer(t *testing.T) {
	RegisterAllPlugins()

	require.Equal(t, inflightload.InFlightLoadProducerType, plugin.DefaultProducerRegistry[attrconcurrency.InFlightLoadKey])
	require.Contains(t, plugin.Registry, inflightload.InFlightLoadProducerType)
}
