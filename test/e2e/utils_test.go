package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
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

// dumpPodsAndLogs dumps all pod statuses and their logs to the Ginkgo writer.
// Call this before cleanup so the information is available when CI tests fail.
func dumpPodsAndLogs() {
	if testConfig == nil || testConfig.KubeCli == nil {
		ginkgo.GinkgoWriter.Println("Skipping pod dump: cluster not initialized")
		return
	}

	ginkgo.GinkgoWriter.Printf("\n=== Dumping pod states and logs (namespace: %s) ===\n", nsName)

	ctx, cancel := context.WithTimeout(testConfig.Context, 30*time.Second)
	defer cancel()

	pods, err := testConfig.KubeCli.CoreV1().Pods(nsName).List(ctx, v1.ListOptions{})
	if err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list pods: %v\n", err)
		return
	}

	ginkgo.GinkgoWriter.Printf("Total pods found: %d\n\n", len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		ginkgo.GinkgoWriter.Printf("--- Pod: %s | Phase: %s | Node: %s ---\n",
			pod.Name, pod.Status.Phase, pod.Spec.NodeName)

		for _, cs := range pod.Status.InitContainerStatuses {
			printContainerStatus("init", cs)
		}
		for _, cs := range pod.Status.ContainerStatuses {
			printContainerStatus("container", cs)
		}

		for _, c := range pod.Spec.InitContainers {
			dumpContainerLogs(ctx, pod.Name, c.Name)
		}
		for _, c := range pod.Spec.Containers {
			dumpContainerLogs(ctx, pod.Name, c.Name)
		}
	}
	ginkgo.GinkgoWriter.Println("=== End of pod dump ===")
}

func printContainerStatus(kind string, cs corev1.ContainerStatus) {
	status := fmt.Sprintf("  [%s] %s | ready=%v restarts=%d", kind, cs.Name, cs.Ready, cs.RestartCount)
	if cs.State.Waiting != nil {
		status += " | Waiting: " + cs.State.Waiting.Reason
	}
	if cs.State.Terminated != nil {
		status += fmt.Sprintf(" | Terminated: %s (exit %d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
	}
	ginkgo.GinkgoWriter.Println(status)
}

func dumpContainerLogs(ctx context.Context, podName, containerName string) {
	tailLines := int64(100)
	req := testConfig.KubeCli.CoreV1().Pods(nsName).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("  [logs] %s/%s: failed to stream logs: %v\n", podName, containerName, err)
		return
	}
	defer stream.Close() //nolint:errcheck

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		ginkgo.GinkgoWriter.Printf("  [logs] %s/%s: failed to read logs: %v\n", podName, containerName, err)
		return
	}
	ginkgo.GinkgoWriter.Printf("  [logs] %s/%s (last 100 lines):\n%s\n", podName, containerName, buf.String())
}
