package models

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	fwkdl "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/datalayer"
	fwkplugin "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

const modelsAttributeKey = "/v1/models"

// ModelInfoCollection defines models' data returned from /v1/models API
type ModelInfoCollection []ModelInfo

// ModelInfo defines model's data returned from /v1/models API
type ModelInfo struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
}

// String returns a string representation of the model info
func (m *ModelInfo) String() string {
	return fmt.Sprintf("%+v", *m)
}

// Clone returns a full copy of the object
func (m ModelInfoCollection) Clone() fwkdl.Cloneable {
	if m == nil {
		return nil
	}
	clone := make([]ModelInfo, len(m))
	copy(clone, m)
	return (*ModelInfoCollection)(&clone)
}

func (m ModelInfoCollection) String() string {
	if m == nil {
		return ""
	}
	parts := make([]string, len(m))
	for i, p := range m {
		parts[i] = p.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ModelResponse is the response from /v1/models API
type ModelResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// ModelsResponseType is the type of models response
var (
	ModelsResponseType = reflect.TypeOf(ModelResponse{})
)

// ModelExtractor implements the models extraction.
type ModelExtractor struct {
	typedName fwkplugin.TypedName
}

// NewModelExtractor returns a new model extractor.
func NewModelExtractor() (*ModelExtractor, error) {
	return &ModelExtractor{
		typedName: fwkplugin.TypedName{
			Type: ModelsExtractorType,
			Name: ModelsExtractorType,
		},
	}, nil
}

// TypedName returns the type and name of the ModelExtractor.
func (me *ModelExtractor) TypedName() fwkplugin.TypedName {
	return me.typedName
}

// ExpectedInputType defines the type expected by ModelExtractor.
func (me *ModelExtractor) ExpectedInputType() reflect.Type {
	return ModelsResponseType
}

// Extract transforms the data source output into a concrete attribute that
// is stored on the given endpoint.
func (me *ModelExtractor) Extract(_ context.Context, data any, ep fwkdl.Endpoint) error {
	models, ok := data.(*ModelResponse)
	if !ok {
		return fmt.Errorf("unexpected input in Extract: %T", data)
	}

	ep.GetAttributes().Put(modelsAttributeKey, ModelInfoCollection(models.Data))
	return nil
}
