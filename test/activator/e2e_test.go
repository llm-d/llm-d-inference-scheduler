package e2e

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

const (
	// simDeployment references the YAML file for the deployment
	simplePrompt   = "Hello my name is Andrew, I have a doctorate in Rocket Science, and I like interplanetary space exploration"
	simDeployment1 = "./yaml/vllm-sim-1.yaml"
	simDeployment2 = "./yaml/vllm-sim-2.yaml"
	modelserver    = "granite-3-8b"
)

var (
	modelName = "granite/granite-3-8b-instruct"
	nsName    = "default"
)

var _ = ginkgo.Describe("Run end to end tests", ginkgo.Ordered, func() {
	ginkgo.When("Running simple non-PD configuration", func() {
		ginkgo.It("should run successfully", func() {
			// Create inferencePool
			inferencePools := createInferencePool(inferExtManifest, "apps/v1", "Deployment", modelserver, "30")

			// Create workload objectts; epp, activator and vLLM pod
			epp := createResource(eppManifest, imageRegistry, eppImg, eppTag)
			activator := createResource(activatorManifest, imageRegistry, activatorImg, activatorTag)

			// Create model server
			modelServers := createResource(simDeployment1, imageRegistry, vllmImg, vllmTag)

			nsHdr, podHdr := runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(modelServers[0]))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, activator)
			testutils.DeleteObjects(testConfig, modelServers)
			testutils.DeleteObjects(testConfig, inferencePools)
		})
	})

	ginkgo.When("Running simple non-PD KV enabled configuration", func() {
		ginkgo.It("should run successfully", func() {
			// Create inferencePool
			inferencePools := createInferencePool(inferExtManifest, "apps/v1", "Deployment", modelserver, "80")

			// Create workload objectts; epp, activator and vLLM pod
			epp := createResource(eppManifest, imageRegistry, eppImg, eppTag)
			activator := createResource(activatorManifest, imageRegistry, activatorImg, activatorTag)

			// Create model server
			modelServers := createResource(simDeployment1, imageRegistry, vllmImg, vllmTag)

			nsHdr, podHdr := runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(modelServers[0]))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, activator)
			testutils.DeleteObjects(testConfig, modelServers)
			testutils.DeleteObjects(testConfig, inferencePools)
		})
	})
})

// createModelServers creates the model server resources used for testing from the given filePaths.
func createInferencePool(inferPoolManifest, apiVersion, kind, name, gracePeriod string) []string {
	manifests := testutils.ReadYaml(inferPoolManifest)
	manifests = substituteMany(manifests,
		map[string]string{
			"${KIND}":         kind,
			"${NAME}":         name,
			"${GRACE_PERIOD}": gracePeriod,
			"${API_VERSION}":  apiVersion,
		})
	objects := CreateObjsFromYaml(testConfig, manifests)

	return objects
}

func createResource(manifest, registry, img, tag string) []string {
	ginkgo.By("Creating resource from manifest: " + manifest)
	objYamls := testutils.ReadYaml(manifest)

	objYamls = substituteMany(objYamls,
		map[string]string{
			"${IMAGE}":          img,
			"${TAG}":            tag,
			"${IMAGE_REGISTRY}": registry,
		})
	objNames := CreateObjsFromYaml(testConfig, objYamls)
	return objNames
}

func runChatCompletion(prompt string) (string, string) {
	var httpResp *http.Response
	openaiclient := openai.NewClient(
		option.WithBaseURL(fmt.Sprintf("http://localhost:%s/v1", port)))

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		Model: modelName,
	}
	resp, err := openaiclient.Chat.Completions.New(testConfig.Context, params, option.WithResponseInto(&httpResp))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal("stop"))
	gomega.Expect(resp.Choices[0].Message.Content).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")

	return namespaceHeader, podHeader
}

func substituteMany(inputs []string, substitutions map[string]string) []string {
	outputs := []string{}
	for _, input := range inputs {
		output := input
		for key, value := range substitutions {
			output = strings.ReplaceAll(output, key, value)
		}
		outputs = append(outputs, output)
	}
	return outputs
}
