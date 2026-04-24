// Package models
package models

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/datalayer"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/http"
)

func TestDatasource(t *testing.T) {
	source, err := http.NewHTTPDataSource("https", "/models", true, ModelsDataSourceType,
		"models-data-source", parseModels, ModelsResponseType)
	assert.Nil(t, err, "failed to create http datasource")
	extractor, err := NewModelExtractor()
	assert.Nil(t, err, "failed to create extractor")

	cfg := &datalayer.Config{
		Sources: []datalayer.DataSourceConfig{
			{
				Plugin:     source,
				Extractors: []fwkdl.Extractor{extractor},
			},
		},
	}

	pollingInterval := 50 * time.Millisecond
	runtime := datalayer.NewRuntime(pollingInterval)

	err = runtime.Configure(cfg, true, "", logr.Logger{})
	assert.Nil(t, err, "failed to configure runtime")

	ctx := context.Background()
	pod := &fwkdl.EndpointMetadata{
		NamespacedName: types.NamespacedName{
			Name:      "pod1",
			Namespace: "default",
		},
		Address: "1.2.3.4:5678",
	}

	endpoint := runtime.NewEndpoint(ctx, pod, nil)
	assert.NotNil(t, endpoint, "failed to create endpoint")

	data, err := source.Poll(ctx, endpoint)
	assert.NotNil(t, err, "expected to fail to collect metrics")
	assert.Nil(t, data)
}
