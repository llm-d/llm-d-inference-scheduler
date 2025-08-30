package e2e

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthPb "google.golang.org/grpc/health/grpc_health_v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// simDeployment references the YAML file for the deployment
	// running the vLLM simulator without PD
	simDeployment = "./yaml/vllm-sim.yaml"
	// simPdDeployment references the YAML file for the deployment
	// running the vLLM simulator with PD
	simPdDeployment = "./yaml/vllm-sim-pd.yaml"

	simplePrompt = "Hello my name is Andrew, I have a doctorate in Rocket Science, and I like interplanetary space exploration"
	extraPrompt  = "Why is the sky sometimes blue and sometimes red close to sunset?"
)

var (
	poolName        = modelName + "-inference-pool"
	podSelector     = map[string]string{"app": poolName}
	prefillSelector = map[string]string{"llm-d.ai/role": "prefill"}
	decodeSelector  = map[string]string{"llm-d.ai/role": "decode"}
)

var _ = ginkgo.Describe("Run end to end tests", ginkgo.Ordered, func() {
	ginkgo.When("Running simple non-PD configuration", func() {
		ginkgo.It("should run successfully", func() {
			modelServers := createModelServers(false, false, 1, 0, 0)

			epp := createEndPointPicker(simpleConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			nsHdr, podHdr := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			nsHdr, podHdr = runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			deleteObjects(epp)
			deleteObjects(modelServers)
		})
	})

	ginkgo.When("Running a PD configuration", func() {
		ginkgo.It("should run successfully", func() {
			prefillReplicas := 1
			decodeReplicas := 4
			modelServers := createModelServers(true, false, 0, prefillReplicas, decodeReplicas)

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			nsHdr, podHdrCompletion := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrCompletion).Should(gomega.BeElementOf(decodePods))

			nsHdr, podHdrChat := runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrChat).Should(gomega.BeElementOf(decodePods))

			// Do an extra completion call with a different prompt
			nsHdr, podHdr := runCompletion(extraPrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run completion with the original prompt
			nsHdr, podHdr = runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(podHdr).Should(gomega.Equal(podHdrCompletion))

			// Do an extra chat completion call with a different prompt
			nsHdr, podHdr = runChatCompletion(extraPrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run chat completion with the original prompt
			nsHdr, podHdr = runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(podHdr).Should(gomega.Equal(podHdrChat))

			deleteObjects(epp)
			deleteObjects(modelServers)
		})
	})

	ginkgo.When("Running simple non-PD KV enabled configuration", func() {
		ginkgo.It("should run successfully", func() {
			epp := createEndPointPicker(kvConfig)

			modelServers := createModelServers(false, true, 1, 0, 0)
			time.Sleep(5 * time.Second) // wait for model server(s) to become ready

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			for range 5 {
				nsHdr, podHdr := runCompletion(simplePrompt, kvModelName)
				gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))
			}

			deleteObjects(epp)
			deleteObjects(modelServers)
		})
	})
})

// createModelServers creates the model server resources used for testing from the given filePaths.
func createModelServers(withPD, withKV bool, vllmReplicas, prefillReplicas, decodeReplicas int) []string {
	theModelName := modelName
	theSafeModelName := modelName
	if withKV {
		theModelName = kvModelName
		theSafeModelName = safeKvModelName
	}
	yaml := simDeployment
	if withPD {
		yaml = simPdDeployment
	}

	manifests := readYaml(yaml)
	manifests = substituteMany(manifests,
		map[string]string{
			"${MODEL_NAME}":           theModelName,
			"${MODEL_NAME_SAFE}":      theSafeModelName,
			"${POOL_NAME}":            poolName,
			"${KV_CACHE_ENABLED}":     strconv.FormatBool(withKV),
			"${ROUTING_SIDECAR_TAG}":  routingSideCarTag,
			"${VLLM_REPLICA_COUNT}":   strconv.Itoa(vllmReplicas),
			"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
			"${VLLM_REPLICA_COUNT_P}": strconv.Itoa(prefillReplicas),
			"${VLLM_SIMULATOR_TAG}":   vllmSimTag,
		})

	objects := createObjsFromYaml(manifests)
	podsInDeploymentsReady(objects)

	return objects
}

func createEndPointPicker(eppConfig string) []string {
	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "epp-config",
			Namespace: nsName,
		},
		Data: map[string]string{"epp-config.yaml": eppConfig},
	}
	err := k8sClient.Create(ctx, configMap)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	objects := []string{"ConfigMap/epp-config"}

	eppYamls := readYaml(eppManifest)
	eppYamls = substituteMany(eppYamls,
		map[string]string{
			"${EPP_TAG}":   eppTag,
			"${POOL_NAME}": modelName + "-inference-pool",
		})

	objects = append(objects, createObjsFromYaml(eppYamls)...)
	podsInDeploymentsReady(objects)

	ginkgo.By("Waiting for EPP to report that it is serving")
	conn, err := grpc.NewClient("localhost:30081",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := conn.Close()
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	}()
	client := healthPb.NewHealthClient(conn)
	healthCheckReq := &healthPb.HealthCheckRequest{}

	gomega.Eventually(func() bool {
		resp, err := client.Check(ctx, healthCheckReq)
		return err == nil && resp.Status == healthPb.HealthCheckResponse_SERVING
	}, 40*time.Second, 2*time.Second).Should(gomega.BeTrue())
	ginkgo.By("EPP reports that it is serving")

	return objects
}

func runCompletion(prompt string, theModel openai.CompletionNewParamsModel) (string, string) {
	var httpResp *http.Response
	openaiclient := openai.NewClient(
		option.WithBaseURL(fmt.Sprintf("http://localhost:%s/v1", port)))

	completionParams := openai.CompletionNewParams{
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String(prompt),
		},
		Model: theModel,
	}

	resp, err := openaiclient.Completions.New(ctx, completionParams, option.WithResponseInto(&httpResp))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal(openai.CompletionChoiceFinishReasonStop))
	gomega.Expect(resp.Choices[0].Text).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")

	return namespaceHeader, podHeader
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
	resp, err := openaiclient.Chat.Completions.New(ctx, params, option.WithResponseInto(&httpResp))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal("stop"))
	gomega.Expect(resp.Choices[0].Message.Content).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")

	return namespaceHeader, podHeader
}

// Simple EPP configuration for running without P/D
const simpleConfig = `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: prefix-cache-scorer
  parameters:
    hashBlockSize: 10
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 256
- type: decode-filter
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 2
`

// EPP configuration for running with P/D
const pdConfig = `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: prefill-header-handler
- type: prefix-cache-scorer
  parameters:
    hashBlockSize: 10
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 256
- type: prefill-filter
- type: decode-filter
- type: max-score-picker
- type: pd-profile-handler
  parameters:
    threshold: 10
schedulingProfiles:
- name: prefill
  plugins:
  - pluginRef: prefill-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 2
- name: decode
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: prefix-cache-scorer
    weight: 2
`

// EPP config for running with precise prefix scoring (i.e. KV events)
const kvConfig = `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: precise-prefix-cache-scorer
  parameters:
    kvEventsConfig:
      zmqEndpoint: tcp://0.0.0.0:5557
    indexerConfig:
      prefixStoreConfig:
        blockSize: 16 
      tokenProcessorConfig:
        blockSize: 16                         # must match vLLM block size if not default (16)
        hashSeed: "42"                        # must match PYTHONHASHSEED in vLLM pods
      tokenizersPoolConfig:
        tokenizersCacheDir: "/cache/tokenizers"
      kvBlockIndexConfig:
        enableMetrics: false                  # enable kv-block index metrics (prometheus)
        metricsLoggingInterval: 6000000000    # log kv-block metrics as well (1m in nanoseconds)
- type: decode-filter
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: precise-prefix-cache-scorer
    weight: 10
`
