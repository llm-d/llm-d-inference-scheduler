package models

import (
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/datalayer/source/http"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

const (
	// ModelsDataSourceType is models data source type
	ModelsDataSourceType = "models-data-source"
	// ModelsExtractorType is models extractor type
	ModelsExtractorType = "model-server-protocol-models"
)

// Configuration parameters for models data source.
type modelsDatasourceParams struct {
	// Scheme defines the protocol scheme used in models retrieval (e.g., "http").
	Scheme string `json:"scheme"`
	// Path defines the URL path used in models retrieval (e.g., "/v1/models").
	Path string `json:"path"`
	// InsecureSkipVerify defines whether model server certificate should be verified or not.
	InsecureSkipVerify bool `json:"insecureSkipVerify"`
}

// ModelDataSourceFactory is a factory function used to instantiate data layer's
// models data source plugins specified in a configuration.
func ModelDataSourceFactory(name string, parameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	cfg := defaultDataSourceConfigParams()
	if parameters != nil { // overlay the defaults with configured values
		if err := json.Unmarshal(parameters, cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Scheme != "http" && cfg.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", cfg.Scheme)
	}

	ds, err := http.NewHTTPDataSource(cfg.Scheme, cfg.Path, cfg.InsecureSkipVerify, ModelsDataSourceType,
		name, parseModels, ModelsResponseType)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP data source: %w", err)
	}
	return ds, nil
}

// ModelServerExtractorFactory is a factory function used to instantiate data layer's models
// Extractor plugins specified in a configuration.
func ModelServerExtractorFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	extractor, err := NewModelExtractor()
	if err != nil {
		return nil, err
	}
	return extractor.WithName(name), nil
}

func defaultDataSourceConfigParams() *modelsDatasourceParams {
	return &modelsDatasourceParams{Scheme: "http", Path: "/v1/models", InsecureSkipVerify: true}
}

func parseModels(data io.Reader) (any, error) {
	body, err := io.ReadAll(data)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}
	var modelsResponse ModelResponse
	err = json.Unmarshal(body, &modelsResponse)
	return &modelsResponse, err
}
