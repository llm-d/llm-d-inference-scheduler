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
	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

const (
	// simDeployment references the YAML file for the deployment
	// running the vLLM simulator without PD
	simDeployment = "./yaml/vllm-sim.yaml"
	// simPDDeployment references the YAML file for the deployment
	// running the vLLM simulator with PD
	simPDDeployment = "./yaml/vllm-sim-pd.yaml"
	// simDPDeployment references  the YAML file for the deployment
	// running the vLLM simulator with Data Parallel
	simDPDeployment = "./yaml/vllm-sim-dp.yaml"

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
			infPoolObjects = createInferencePool(1, true)

			modelServers := createModelServers(false, false, false, 1, 0, 0)

			epp := createEndPointPicker(simpleConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			nsHdr, podHdr, _ := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			nsHdr, podHdr, _ = runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})

	ginkgo.When("Running a PD configuration", func() {
		ginkgo.It("should run successfully", func() {
			infPoolObjects = createInferencePool(1, true)

			prefillReplicas := 1
			decodeReplicas := 4
			modelServers := createModelServers(true, false, false, 0, prefillReplicas, decodeReplicas)

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			nsHdr, podHdrCompletion, _ := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrCompletion).Should(gomega.BeElementOf(decodePods))

			nsHdr, podHdrChat, _ := runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrChat).Should(gomega.BeElementOf(decodePods))

			// Do an extra completion call with a different prompt
			nsHdr, podHdr, _ := runCompletion(extraPrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run completion with the original prompt
			nsHdr, podHdr, _ = runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(podHdr).Should(gomega.Equal(podHdrCompletion))

			// Do an extra chat completion call with a different prompt
			nsHdr, podHdr, _ = runChatCompletion(extraPrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run chat completion with the original prompt
			nsHdr, podHdr, _ = runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(podHdr).Should(gomega.Equal(podHdrChat))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})

	ginkgo.When("Running simple non-PD KV enabled configuration", func() {
		ginkgo.It("should run successfully", func() {
			infPoolObjects = createInferencePool(1, true)

			epp := createEndPointPicker(kvConfig)

			modelServers := createModelServers(false, true, false, 1, 0, 0)
			time.Sleep(5 * time.Second) // wait for model server(s) to become ready

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			for range 5 {
				nsHdr, podHdr, _ := runCompletion(simplePrompt, kvModelName)
				gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))
			}

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})

	ginkgo.When("Scaling up and down the model servers", func() {
		ginkgo.It("should distribute inference requests across all model servers", func() {
			infPoolObjects = createInferencePool(1, true)

			modelServers := createModelServers(false, false, false, 1, 0, 0)

			epp := createEndPointPicker(scaleConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			var nsHdr, podHdr string
			for range 5 {
				nsHdr, podHdr, _ = runCompletion(simplePrompt, modelName)
				gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))
			}

			scaleDeployment(modelServers, 1)

			scaledUpPrefillPods, scaledUpDecodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(scaledUpPrefillPods).Should(gomega.BeEmpty())
			gomega.Expect(scaledUpDecodePods).Should(gomega.HaveLen(2))

			var scaledNsHdr, scaledPodHdr string
			// Run inference multiple times until one is scheduled on the new pod
			for range 30 {
				scaledNsHdr, scaledPodHdr, _ = runCompletion(extraPrompt, modelName)
				gomega.Expect(scaledNsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(scaledPodHdr).Should(gomega.BeElementOf(scaledUpDecodePods))
				if scaledPodHdr != podHdr {
					break
				}
			}
			gomega.Expect(scaledPodHdr).ShouldNot(gomega.Equal(podHdr))

			scaleDeployment(modelServers, -1)

			scaledDownPrefillPods, scaledDownDecodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(scaledDownPrefillPods).Should(gomega.BeEmpty())
			gomega.Expect(scaledDownDecodePods).Should(gomega.HaveLen(1))
			gomega.Expect(scaledDownDecodePods[0]).Should(gomega.BeElementOf(scaledUpDecodePods))

			// Run multiple times and insure that they are scheduled on the remaining pod
			for range 5 {
				nsHdr, podHdr, _ = runCompletion(simplePrompt, modelName)
				gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(podHdr).Should(gomega.Equal(scaledDownDecodePods[0]))
			}

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})

	ginkgo.When("Running a vLLM Data Parallel configuration", func() {
		ginkgo.It("should schedule inference on all ranks", func() {
			infPoolObjects = createInferencePool(2, true)

			modelServers := createModelServers(false, false, true, 1, 0, 0)

			epp := createEndPointPicker(dataParallelConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.BeEmpty())
			gomega.Expect(decodePods).Should(gomega.HaveLen(1))

			nsHdr, podHdr, portHdr := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			var parallelNsHdr, parallelPodHdr, parallelPortHdr string

			// Run inference multiple times until one is scheduled on the other port
			for range 30 {
				parallelNsHdr, parallelPodHdr, parallelPortHdr = runCompletion(extraPrompt, modelName)
				gomega.Expect(parallelNsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(parallelPodHdr).Should(gomega.Equal(decodePods[0]))
				if parallelPortHdr != portHdr {
					break
				}
			}
			gomega.Expect(parallelPortHdr).ShouldNot(gomega.Equal(portHdr))

			nsHdr, podHdr, portHdr = runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.Equal(decodePods[0]))

			// Run inference multiple times until one is scheduled on the other port
			for range 30 {
				parallelNsHdr, parallelPodHdr, parallelPortHdr = runChatCompletion(extraPrompt)
				gomega.Expect(parallelNsHdr).Should(gomega.Equal(nsName))
				gomega.Expect(parallelPodHdr).Should(gomega.Equal(decodePods[0]))
				if parallelPortHdr != portHdr {
					break
				}
			}
			gomega.Expect(parallelPortHdr).ShouldNot(gomega.Equal(portHdr))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})
})

// createModelServers creates the model server resources used for testing from the given filePaths.
func createModelServers(withPD, withKV, withDP bool, vllmReplicas, prefillReplicas, decodeReplicas int) []string {
	theModelName := modelName
	theSafeModelName := modelName
	if withKV {
		theModelName = kvModelName
		theSafeModelName = safeKvModelName
	}
	yaml := simDeployment
	if withPD {
		yaml = simPDDeployment
	} else if withDP {
		yaml = simDPDeployment
	}

	manifests := testutils.ReadYaml(yaml)
	manifests = substituteMany(manifests,
		map[string]string{
			"${MODEL_NAME}":           theModelName,
			"${MODEL_NAME_SAFE}":      theSafeModelName,
			"${POOL_NAME}":            poolName,
			"${KV_CACHE_ENABLED}":     strconv.FormatBool(withKV),
			"${SIDECAR_IMAGE}":        sideCarImage,
			"${VLLM_REPLICA_COUNT}":   strconv.Itoa(vllmReplicas),
			"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
			"${VLLM_REPLICA_COUNT_P}": strconv.Itoa(prefillReplicas),
			"${VLLM_SIMULATOR_IMAGE}": vllmSimImage,
		})

	objects := testutils.CreateObjsFromYaml(testConfig, manifests)
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
	err := testConfig.K8sClient.Create(testConfig.Context, configMap)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	objects := []string{"ConfigMap/epp-config"}

	eppYamls := testutils.ReadYaml(eppManifest)
	eppYamls = substituteMany(eppYamls,
		map[string]string{
			"${EPP_IMAGE}": eppImage,
			"${NAMESPACE}": nsName,
			"${POOL_NAME}": modelName + "-inference-pool",
		})

	objects = append(objects, testutils.CreateObjsFromYaml(testConfig, eppYamls)...)
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
		resp, err := client.Check(testConfig.Context, healthCheckReq)
		return err == nil && resp.Status == healthPb.HealthCheckResponse_SERVING
	}, 40*time.Second, 2*time.Second).Should(gomega.BeTrue())
	ginkgo.By("EPP reports that it is serving")
	time.Sleep(2 * time.Second)

	return objects
}

func runCompletion(prompt string, theModel openai.CompletionNewParamsModel) (string, string, string) {
	var httpResp *http.Response
	openaiclient := openai.NewClient(
		option.WithBaseURL(fmt.Sprintf("http://localhost:%s/v1", port)))

	completionParams := openai.CompletionNewParams{
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String(prompt),
		},
		Model: theModel,
	}

	ginkgo.By(fmt.Sprintf("Sending Completion Request: (port %s) %#v", port, completionParams))

	resp, err := openaiclient.Completions.New(testConfig.Context, completionParams, option.WithResponseInto(&httpResp), option.WithRequestTimeout(readyTimeout))

	ginkgo.By(fmt.Sprintf("Verifying Completion Response: %#v", resp))

	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal(openai.CompletionChoiceFinishReasonStop))
	gomega.Expect(resp.Choices[0].Text).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")
	podPort := httpResp.Header.Get("x-inference-port")

	return namespaceHeader, podHeader, podPort
}

func runChatCompletion(prompt string) (string, string, string) {
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
	podPort := httpResp.Header.Get("x-inference-port")

	return namespaceHeader, podHeader, podPort
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
        hf:
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

// EPP configuration for running scale model server test
const scaleConfig = `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: max-score-picker
`

// EPP configuration for running with vLLM Data Parallel support
const dataParallelConfig = `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: decode-filter
- type: max-score-picker
- type: data-parallel-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
`
