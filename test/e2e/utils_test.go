package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apilabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	deploymentKind = "deployment"
)

func scaleDeployment(objects []string, increment int) {
	direction := "up"
	absIncrement := increment
	if increment < 0 {
		direction = "down"
		absIncrement = -increment
	}

	for _, kindAndName := range objects {
		split := strings.Split(kindAndName, "/")
		if strings.ToLower(split[0]) == deploymentKind {
			ginkgo.By(fmt.Sprintf("Scaling the deployment %s %s by %d", split[1], direction, absIncrement))
			scale, err := testConfig.KubeCli.AppsV1().Deployments(nsName).GetScale(testConfig.Context, split[1], v1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			scale.Spec.Replicas += int32(increment)
			_, err = testConfig.KubeCli.AppsV1().Deployments(nsName).UpdateScale(testConfig.Context, split[1], scale, v1.UpdateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
	podsInDeploymentsReady(objects)
}

// getModelServerPods Returns the list of Prefill and Decode vLLM pods separately
func getModelServerPods(podLabels, prefillLabels, decodeLabels map[string]string) ([]string, []string) {
	ginkgo.By("Getting Model server pods")

	pods := getPods(podLabels)

	prefillValidator, err := apilabels.ValidatedSelectorFromSet(prefillLabels)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	decodeValidator, err := apilabels.ValidatedSelectorFromSet(decodeLabels)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	prefillPods := []string{}
	decodePods := []string{}

	for _, pod := range pods {
		podLabels := apilabels.Set(pod.Labels)
		switch {
		case prefillValidator.Matches(podLabels):
			prefillPods = append(prefillPods, pod.Name)
		case decodeValidator.Matches(podLabels):
			decodePods = append(decodePods, pod.Name)
		default:
			// If not labelled at all, it's a decode pod
			notFound := true
			for decodeKey := range decodeLabels {
				if _, ok := pod.Labels[decodeKey]; ok {
					notFound = false
					break
				}
			}
			if notFound {
				decodePods = append(decodePods, pod.Name)
			}
		}
	}

	return prefillPods, decodePods
}

func getPods(labels map[string]string) []corev1.Pod {
	podList := corev1.PodList{}
	selector := apilabels.SelectorFromSet(labels)
	err := testConfig.K8sClient.List(testConfig.Context, &podList, &client.ListOptions{LabelSelector: selector})
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	pods := []corev1.Pod{}
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			pods = append(pods, pod)
		}
	}

	return pods
}

// getPodNames returns the names of all running pods matching the given label selector.
func getPodNames(labels map[string]string) []string {
	pods := getPods(labels)
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

func podsInDeploymentsReady(objects []string) {
	isDeploymentReady := func(deploymentName string) bool {
		var deployment appsv1.Deployment
		err := testConfig.K8sClient.Get(testConfig.Context, types.NamespacedName{Namespace: nsName, Name: deploymentName}, &deployment)
		ginkgo.By(fmt.Sprintf("Waiting for deployment %q to be ready (err: %v): replicas=%#v, status=%#v", deploymentName, err, *deployment.Spec.Replicas, deployment.Status))
		return err == nil && *deployment.Spec.Replicas == deployment.Status.Replicas &&
			deployment.Status.Replicas == deployment.Status.ReadyReplicas
	}

	for _, kindAndName := range objects {
		split := strings.Split(kindAndName, "/")
		if strings.ToLower(split[0]) == deploymentKind {
			gomega.Eventually(isDeploymentReady).
				WithArguments(split[1]).
				WithPolling(interval).
				WithTimeout(readyTimeout).
				Should(gomega.BeTrue())
		}
	}
}

func runKustomize(kustomizeDir string) []string {
	command := exec.Command("kustomize", "build", kustomizeDir)
	session, err := gexec.Start(command, nil, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
	return strings.Split(string(session.Out.Contents()), "\n---")
}

func substituteMany(inputs []string, substitutions map[string]string) []string {
	outputs := make([]string, len(inputs))
	for idx, input := range inputs {
		output := input
		for key, value := range substitutions {
			output = strings.ReplaceAll(output, key, value)
		}
		outputs[idx] = output
	}
	return outputs
}

// getCounterMetric fetches the current value of a Prometheus counter metric from the given metrics URL.
//
//nolint:unparam // metricName may vary in future test cases
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
