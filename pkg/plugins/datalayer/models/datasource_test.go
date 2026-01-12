// Package models
package models

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer/http"
)

func TestDatasource(t *testing.T) {
	source := http.NewHTTPDataSource("https", "/models", true, ModelsDataSourceType,
		"models-data-source", parseModels, ModelsResponseType)
	extractor, err := NewModelExtractor()
	assert.Nil(t, err, "failed to create extractor")

	dsType := source.TypedName().Type
	assert.Equal(t, ModelsDataSourceType, dsType)

	err = source.AddExtractor(extractor)
	assert.Nil(t, err, "failed to add extractor")

	err = source.AddExtractor(extractor)
	assert.NotNil(t, err, "expected to fail to add the same extractor twice")

	extractors := source.Extractors()
	assert.Len(t, extractors, 1)
	assert.Equal(t, extractor.TypedName().String(), extractors[0])

	err = datalayer.RegisterSource(source)
	assert.Nil(t, err, "failed to register")

	ctx := context.Background()
	factory := datalayer.NewEndpointFactory([]datalayer.DataSource{source}, 100*time.Millisecond)
	pod := &datalayer.EndpointMetadata{
		NamespacedName: types.NamespacedName{
			Name:      "pod1",
			Namespace: "default",
		},
		Address: "1.2.3.4:5678",
	}
	endpoint := factory.NewEndpoint(ctx, pod, nil)
	assert.NotNil(t, endpoint, "failed to create endpoint")

	err = source.Collect(ctx, endpoint)
	assert.NotNil(t, err, "expected to fail to collect metrics")
}
