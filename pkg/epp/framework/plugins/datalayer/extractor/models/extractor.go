package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

const (
	ModelsAttributeKey  = "/v1/models"
	ModelsExtractorType = "models-data-extractor"
)

// ModelDataCollection defines models' data returned from /v1/models API
type ModelDataCollection []ModelData

// ModelData defines model's data returned from /v1/models API
type ModelData struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
}

// String returns a string representation of the model info
func (m *ModelData) String() string {
	return fmt.Sprintf("%+v", *m)
}

// Clone returns a full copy of the object
func (m ModelDataCollection) Clone() fwkdl.Cloneable {
	if m == nil {
		return nil
	}
	clone := make([]ModelData, len(m))
	copy(clone, m)
	return (*ModelDataCollection)(&clone)
}

func (m ModelDataCollection) String() string {
	if m == nil {
		return "[]"
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
	Data   []ModelData `json:"data"`
}

// ModelExtractor implements the models extraction.
type ModelExtractor struct {
	typedName fwkplugin.TypedName
}

// NewModelExtractor returns a new model extractor.
func NewModelExtractor() *ModelExtractor {
	return &ModelExtractor{
		typedName: fwkplugin.TypedName{
			Type: ModelsExtractorType,
			Name: ModelsExtractorType,
		},
	}
}

// TypedName returns the type and name of the ModelExtractor.
func (me *ModelExtractor) TypedName() fwkplugin.TypedName {
	return me.typedName
}

// ModelServerExtractorFactory instantiates the models extractor plugin.
func ModelServerExtractorFactory(name string, _ json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	extractor := NewModelExtractor()
	extractor.typedName.Name = name
	return extractor, nil
}

// Extract transforms the data source output into a concrete attribute stored
// on the input's endpoint.
func (me *ModelExtractor) Extract(_ context.Context, input fwkdl.PollingInput) error {
	models, ok := input.Data.(*ModelResponse)
	if !ok {
		return fmt.Errorf("unexpected input in Extract: %T", input.Data)
	}
	if input.Endpoint == nil {
		return errors.New("Extract called without an endpoint")
	}
	input.Endpoint.GetAttributes().Put(ModelsAttributeKey, ModelDataCollection(models.Data))
	return nil
}
