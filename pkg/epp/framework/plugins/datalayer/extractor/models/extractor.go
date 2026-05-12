package models

import (
	"context"
	"encoding/json"
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

// ModelResponse is the response from /v1/models API.
// Data is typed as ModelDataCollection (not []ModelData) so the Cloneable
// methods are attached directly, removing the per-call ModelDataCollection
// conversion the extractor would otherwise need.
type ModelResponse struct {
	Object string              `json:"object"`
	Data   ModelDataCollection `json:"data"`
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

// ModelServerExtractorFactory is a factory function used to instantiate data layer's
// models extractor plugins specified in a configuration.
func ModelServerExtractorFactory(name string, _ json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	extractor := NewModelExtractor()
	extractor.typedName.Name = name
	return extractor, nil
}

// Extract transforms the typed models payload into endpoint attributes.
// Payload is non-nil per the PollingDispatcher contract: a conforming
// dispatcher short-circuits on nil poll results before invoking extractors.
func (me *ModelExtractor) Extract(_ context.Context, in fwkdl.PollInput[*ModelResponse]) error {
	in.Endpoint.GetAttributes().Put(ModelsAttributeKey, in.Payload.Data)
	return nil
}
