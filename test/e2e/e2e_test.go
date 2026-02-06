package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	// running the vLLM simulator with PD (connector type is configurable via ${CONNECTOR_TYPE})
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

			metricsURL := fmt.Sprintf("http://localhost:%s/metrics", metricsPort)

			if k8sContext != "" {
				// Use port-forward to access the EPP pod's metrics endpoint.
				startEPPMetricsPortForward()
			}

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

			// Metrics Validation
			labelFilter := fmt.Sprintf(`decision_type="prefill-decode",model_name="%s"`, modelName)
			prefillDecodeCount := getCounterMetric(metricsURL, "llm_d_inference_scheduler_pd_decision_total", labelFilter)

			labelFilter2 := fmt.Sprintf(`decision_type="decode-only",model_name="%s"`, modelName)
			decodeOnlyCount := getCounterMetric(metricsURL, "llm_d_inference_scheduler_pd_decision_total", labelFilter2)

			gomega.Expect(prefillDecodeCount).Should(gomega.Equal(6))
			gomega.Expect(decodeOnlyCount).Should(gomega.Equal(0))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})
	})

	ginkgo.When("Running a PD configuration with shared-storage connector", func() {
		ginkgo.It("should run regular (non-streaming) requests successfully", func() {
			infPoolObjects = createInferencePool(1, true)

			prefillReplicas := 1
			decodeReplicas := 2
			modelServers := createModelServersWithConnector(true, false, false, 0, prefillReplicas, decodeReplicas, "shared-storage")

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			// Test regular completion request
			nsHdr, podHdrCompletion, _ := runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrCompletion).Should(gomega.BeElementOf(decodePods))

			// Test regular chat completion request
			nsHdr, podHdrChat, _ := runChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdrChat).Should(gomega.BeElementOf(decodePods))

			// Run completion with a different prompt
			nsHdr, podHdr, _ := runCompletion(extraPrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run completion with original prompt (should go to same pod due to prefix cache)
			nsHdr, podHdr, _ = runCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(podHdr).Should(gomega.Equal(podHdrCompletion))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})

		ginkgo.It("should run streaming requests successfully", func() {
			infPoolObjects = createInferencePool(1, true)

			prefillReplicas := 1
			decodeReplicas := 2
			modelServers := createModelServersWithConnector(true, false, false, 0, prefillReplicas, decodeReplicas, "shared-storage")

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			// Test streaming completion request
			nsHdr, podHdr := runStreamingCompletion(simplePrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Test streaming chat completion request
			nsHdr, podHdr = runStreamingChatCompletion(simplePrompt)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			// Run streaming completion with a different prompt
			nsHdr, podHdr = runStreamingCompletion(extraPrompt, modelName)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})

		ginkgo.It("should handle decode-first success scenario with cache_hit_threshold", func() {
			// This test verifies the decode-first optimization:
			// When cache_hit_threshold is set and the decode succeeds (cache hit),
			// the request should complete without falling back to P/D.
			// IMPORTANT: The prefill pod should NOT process any requests in this scenario.
			infPoolObjects = createInferencePool(1, true)

			prefillReplicas := 1
			decodeReplicas := 2
			modelServers := createModelServersWithConnector(true, false, false, 0, prefillReplicas, decodeReplicas, "shared-storage")

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			// Get prefill request count BEFORE the test
			prefillCountBefore := getPrefillRequestCount(prefillPods[0])
			ginkgo.By(fmt.Sprintf("Prefill request count before decode-first test: %d", prefillCountBefore))

			// Test decode-first success: cache_hit_threshold is set, but simulator returns "stop"
			// (without X-Cache-Threshold header), meaning decode succeeded without prefill
			nsHdr, podHdr, finishReason := runCompletionWithCacheThreshold(simplePrompt, 0.5, false)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(finishReason).ShouldNot(gomega.Equal("cache_threshold"))

			// Test streaming decode-first success
			nsHdr, podHdr, finishReason = runStreamingCompletionWithCacheThreshold(simplePrompt, 0.5, false)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(finishReason).ShouldNot(gomega.Equal("cache_threshold"))

			// Get prefill request count AFTER the test
			prefillCountAfter := getPrefillRequestCount(prefillPods[0])
			ginkgo.By(fmt.Sprintf("Prefill request count after decode-first test: %d", prefillCountAfter))

			// VERIFY: Prefill pod should NOT have processed any new requests
			// (decode-first succeeded, so no P/D fallback occurred)
			gomega.Expect(prefillCountAfter).Should(gomega.Equal(prefillCountBefore),
				"Prefill pod should NOT process requests when cache threshold is met (decode-first success)")

			testutils.DeleteObjects(testConfig, epp)
			testutils.DeleteObjects(testConfig, modelServers)
		})

		ginkgo.It("should handle decode-first fallback to P/D when cache threshold not met", func() {
			// This test verifies the decode-first fallback scenario:
			// When cache_hit_threshold is set and the decode returns cache_threshold finish_reason,
			// the sidecar should fall back to P/D disaggregation.
			// IMPORTANT: The prefill pod SHOULD process requests in this scenario.
			infPoolObjects = createInferencePool(1, true)

			prefillReplicas := 1
			decodeReplicas := 2
			modelServers := createModelServersWithConnector(true, false, false, 0, prefillReplicas, decodeReplicas, "shared-storage")

			epp := createEndPointPicker(pdConfig)

			prefillPods, decodePods := getModelServerPods(podSelector, prefillSelector, decodeSelector)
			gomega.Expect(prefillPods).Should(gomega.HaveLen(prefillReplicas))
			gomega.Expect(decodePods).Should(gomega.HaveLen(decodeReplicas))

			// Get prefill request count BEFORE the test
			prefillCountBefore := getPrefillRequestCount(prefillPods[0])
			ginkgo.By(fmt.Sprintf("Prefill request count before P/D fallback test: %d", prefillCountBefore))

			// Test decode-first fallback: cache_hit_threshold is set AND X-Cache-Threshold header
			// forces simulator to return "cache_threshold" finish_reason, triggering P/D fallback
			nsHdr, podHdr, finishReason := runCompletionWithCacheThreshold(simplePrompt, 0.5, true)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			// The sidecar completes the P/D flow but returns cache_threshold as the finish_reason
			// from the initial decode attempt (which triggered the fallback)
			gomega.Expect(finishReason).Should(gomega.Equal("cache_threshold"))

			// Test streaming decode-first fallback
			nsHdr, podHdr, finishReason = runStreamingCompletionWithCacheThreshold(extraPrompt, 0.5, true)
			gomega.Expect(nsHdr).Should(gomega.Equal(nsName))
			gomega.Expect(podHdr).Should(gomega.BeElementOf(decodePods))
			gomega.Expect(finishReason).Should(gomega.Equal("cache_threshold"))

			// Get prefill request count AFTER the test
			prefillCountAfter := getPrefillRequestCount(prefillPods[0])
			ginkgo.By(fmt.Sprintf("Prefill request count after P/D fallback test: %d", prefillCountAfter))

			// VERIFY: Prefill pod SHOULD have processed 2 new requests (1 regular + 1 streaming)
			// (decode-first failed, so P/D fallback occurred and prefill was invoked)
			gomega.Expect(prefillCountAfter).Should(gomega.BeNumerically(">", prefillCountBefore),
				"Prefill pod SHOULD process requests when cache threshold is NOT met (P/D fallback)")
			gomega.Expect(prefillCountAfter-prefillCountBefore).Should(gomega.Equal(2),
				"Prefill pod should have processed exactly 2 requests (1 regular + 1 streaming)")

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
// Uses the default connector (nixlv2) for P/D deployments.
func createModelServers(withPD, withKV, withDP bool, vllmReplicas, prefillReplicas, decodeReplicas int) []string {
	return createModelServersWithConnector(withPD, withKV, withDP, vllmReplicas, prefillReplicas, decodeReplicas, "nixlv2")
}

// createModelServersWithConnector creates model server resources with a specific connector type.
func createModelServersWithConnector(withPD, withKV, withDP bool, vllmReplicas, prefillReplicas, decodeReplicas int, connector string) []string {
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
			"${CONNECTOR_TYPE}":       connector,
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

// getCounterMetric fetches the current value of a Prometheus counter metric from the given metrics URL.
func getCounterMetric(metricsURL, metricName, labelMatch string) int {
	resp, err := http.Get(metricsURL)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err = resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	body, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	metricsText := string(body)
	for _, line := range strings.Split(metricsText, "\n") {
		if strings.HasPrefix(line, metricName) && strings.Contains(line, labelMatch) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				valFloat, err := strconv.ParseFloat(fields[len(fields)-1], 64)
				gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
				return int(valFloat)
			}
		}
	}
	return 0
}

func runStreamingCompletion(prompt string, theModel openai.CompletionNewParamsModel) (string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Completion Request: (port %s) model=%s", port, theModel))

	// Use raw HTTP for streaming to capture headers
	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":50,"stream":true}`, theModel, prompt)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read and verify the streaming response
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	ginkgo.By(fmt.Sprintf("Streaming Completion received response length: %d bytes", len(respBody)))

	return namespaceHeader, podHeader
}

func runStreamingChatCompletion(prompt string) (string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Chat Completion Request: (port %s)", port))

	// Use raw HTTP for streaming to capture headers
	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"%s"}],"stream":true}`, modelName, prompt)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read and verify the streaming response
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	ginkgo.By(fmt.Sprintf("Streaming Chat Completion received response length: %d bytes", len(respBody)))

	return namespaceHeader, podHeader
}

// runCompletionWithCacheThreshold sends a completion request with cache_hit_threshold parameter.
// This triggers the decode-first optimization in the shared-storage connector.
// Returns namespace header, pod header, and the finish reason from the response.
func runCompletionWithCacheThreshold(prompt string, cacheHitThreshold float64, forceCacheThresholdFinishReason bool) (string, string, string) {
	ginkgo.By(fmt.Sprintf("Sending Completion Request with cache_hit_threshold=%v, forceCacheThreshold=%v", cacheHitThreshold, forceCacheThresholdFinishReason))

	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":10,"cache_hit_threshold":%v}`, modelName, prompt, cacheHitThreshold)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	// Add X-Cache-Threshold header to force the simulator to return cache_threshold finish_reason
	if forceCacheThresholdFinishReason {
		req.Header.Set("X-Cache-Threshold-Finish-Reason", "true")
	}

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Parse response to get finish_reason
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	// Extract finish_reason from JSON response
	finishReason := extractFinishReason(string(respBody))

	ginkgo.By(fmt.Sprintf("Completion Response: ns=%s, pod=%s, finish_reason=%s", namespaceHeader, podHeader, finishReason))

	return namespaceHeader, podHeader, finishReason
}

// runStreamingCompletionWithCacheThreshold sends a streaming completion request with cache_hit_threshold.
func runStreamingCompletionWithCacheThreshold(prompt string, cacheHitThreshold float64, forceCacheThresholdFinishReason bool) (string, string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Completion Request with cache_hit_threshold=%v, forceCacheThreshold=%v", cacheHitThreshold, forceCacheThresholdFinishReason))

	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":10,"stream":true,"cache_hit_threshold":%v}`, modelName, prompt, cacheHitThreshold)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	if forceCacheThresholdFinishReason {
		req.Header.Set("X-Cache-Threshold-Finish-Reason", "true")
	}

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read streaming response and extract finish_reason from the last data chunk
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	finishReason := extractFinishReasonFromStreaming(string(respBody))

	ginkgo.By(fmt.Sprintf("Streaming Completion Response: ns=%s, pod=%s, finish_reason=%s", namespaceHeader, podHeader, finishReason))

	return namespaceHeader, podHeader, finishReason
}

// extractFinishReason extracts the finish_reason field from a JSON response string.
func extractFinishReason(jsonStr string) string {
	// Simple extraction - look for "finish_reason":"value" pattern
	idx := strings.Index(jsonStr, `"finish_reason":"`)
	if idx == -1 {
		// Try with null value
		if strings.Contains(jsonStr, `"finish_reason":null`) {
			return "null"
		}
		return ""
	}
	start := idx + len(`"finish_reason":"`)
	end := strings.Index(jsonStr[start:], `"`)
	if end == -1 {
		return ""
	}
	return jsonStr[start : start+end]
}

// extractFinishReasonFromStreaming extracts the finish_reason from the last SSE data chunk.
func extractFinishReasonFromStreaming(sseData string) string {
	// Find the last "finish_reason" that is not null
	lines := strings.Split(sseData, "\n")
	lastFinishReason := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") && !strings.Contains(line, "[DONE]") {
			fr := extractFinishReason(line)
			if fr != "" && fr != "null" {
				lastFinishReason = fr
			}
		}
	}
	return lastFinishReason
}

// getPrefillRequestCount gets the total request count from a prefill pod's metrics endpoint.
// This is used to verify whether a request was processed by the prefill pod.
func getPrefillRequestCount(prefillPodName string) int {
	ginkgo.By("Getting request count from prefill pod: " + prefillPodName)

	// Use Kubernetes API proxy to access the metrics endpoint
	output, err := testConfig.KubeCli.CoreV1().RESTClient().
		Get().
		Namespace(nsName).
		Resource("pods").
		Name(prefillPodName + ":8000").
		SubResource("proxy").
		Suffix("metrics").
		DoRaw(testConfig.Context)
	if err != nil {
		ginkgo.By(fmt.Sprintf("Warning: Could not get metrics from prefill pod %s: %v", prefillPodName, err))
		return -1
	}

	return parseRequestCountFromMetrics(string(output))
}

// parseRequestCountFromMetrics extracts the request count from Prometheus metrics output.
func parseRequestCountFromMetrics(metricsOutput string) int {
	// Look for vllm:e2e_request_latency_seconds_count{model_name="food-review"} <count>
	lines := strings.Split(metricsOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "vllm:e2e_request_latency_seconds_count") &&
			strings.Contains(line, "food-review") {
			// Extract the count value after the last space
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				count, err := strconv.Atoi(parts[len(parts)-1])
				if err == nil {
					return count
				}
			}
		}
	}
	return 0
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
    blockSizeTokens: 10
    maxPrefixBlocksToMatch: 256
    lruCapacityPerServer: 256
- type: prefill-filter
- type: decode-filter
- type: max-score-picker
- type: pd-profile-handler
  parameters:
    hashBlockSize: 10
    threshold: 40
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
    tokenProcessorConfig:
      blockSize: 16 
      hashSeed: "42"
    kvEventsConfig:
      zmqEndpoint: tcp://0.0.0.0:5557
    indexerConfig:
      prefixStoreConfig:
        blockSize: 16 
      tokenizersPoolConfig:
        modelName: Qwen/Qwen2.5-1.5B-Instruct
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
