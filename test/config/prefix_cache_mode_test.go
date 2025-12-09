package config_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/config/loader"
	giePlugins "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/test/utils"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

func TestPrecisePrefixCacheScorer(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		configText string
	}{
		{
			name:       "precise prefix cache scorer",
			pluginName: "precisePrefixCache",
			configText: `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- name: precisePrefixCache
  type: precise-prefix-cache-scorer
  parameters:
    kvEventsConfig:
      zmqEndpoint: "tcp://localhost:5557"
- name: profileHandler
  type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: precisePrefixCache
`,
		},
	}
	ctx := context.Background()
	// Register llm-d-inference-scheduler plugins
	plugins.RegisterAllPlugins()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Setenv("HF_TOKEN", "dummy_token") // needed for cache_tracking
			rawConfig, _, err := loader.LoadConfigPhaseOne([]byte(test.configText), logr.Discard())
			if err != nil {
				t.Fatalf("unexpected error from LoadConfigPhaseOne: %v", err)
			}
			handle := utils.NewTestHandle(ctx)
			_, err = loader.LoadConfigPhaseTwo(rawConfig, handle, logr.Discard())
			if err != nil {
				t.Fatalf("unexpected error from LoadConfigPhaseTwo: %v", err)
			}
			fmt.Println("all plugins", handle.GetAllPluginsWithNames())

			_, err = giePlugins.PluginByType[*scorer.PrecisePrefixCacheScorer](handle, test.pluginName)
			if err != nil {
				t.Fatalf("expected PrecisePrefixCacheScorer, but got error: %v", err)
			}
		})
	}
}
