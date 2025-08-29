package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apilabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/gateway-api-inference-extension/apix/v1alpha2"
	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

func createObjsFromYaml(docs []string) []string {
	objNames := []string{}

	// For each doc, decode and create
	decoder := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	for _, doc := range docs {
		trimmed := strings.TrimSpace(doc)
		if trimmed == "" {
			continue
		}
		// Decode into a runtime.Object
		obj, gvk, decodeErr := decoder.Decode([]byte(trimmed), nil, nil)
		gomega.Expect(decodeErr).NotTo(gomega.HaveOccurred(),
			"Failed to decode YAML document to a Kubernetes object")

		ginkgo.By(fmt.Sprintf("Decoded GVK: %s", gvk))

		unstrObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			// Fallback if it's a typed object
			unstrObj = &unstructured.Unstructured{}
			// Convert typed to unstructured
			err := scheme.Convert(obj, unstrObj, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		unstrObj.SetNamespace(nsName)
		kind := unstrObj.GetKind()
		name := unstrObj.GetName()
		objNames = append(objNames, kind+"/"+name)

		// Create the object
		err := k8sClient.Create(ctx, unstrObj, &client.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(),
			"Failed to create object from YAML")

		// Wait for the created object to exist.
		clientObj := getClientObject(kind)
		testutils.EventuallyExists(ctx, func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: name}, clientObj)
		}, existsTimeout, interval)

		switch kind {
		case "CustomResourceDefinition":
			// Wait for the CRD to be established.
			testutils.CRDEstablished(ctx, k8sClient, clientObj.(*apiextv1.CustomResourceDefinition),
				readyTimeout, interval)
		case "Deployment":
			// Wait for the deployment to be available.
			testutils.DeploymentAvailable(ctx, k8sClient, clientObj.(*appsv1.Deployment),
				modelReadyTimeout, interval)
		}
	}
	return objNames
}

func deleteObjects(kindAndNames []string) {
	for _, kindAndName := range kindAndNames {
		split := strings.Split(kindAndName, "/")
		clientObj := getClientObject(split[0])
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: split[1]}, clientObj)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = k8sClient.Delete(ctx, clientObj)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func() bool {
			clientObj := getClientObject(split[0])
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: split[1]}, clientObj)
			return apierrors.IsNotFound(err)
		}, existsTimeout, interval).Should(gomega.BeTrue())
	}
}

func getClientObject(kind string) client.Object {
	switch strings.ToLower(kind) {
	case "configmap":
		return &corev1.ConfigMap{}
	case "customresourcedefinition":
		return &apiextv1.CustomResourceDefinition{}
	case "deployment":
		return &appsv1.Deployment{}
	case "inferencepool":
		return &v1alpha2.InferencePool{}
	case "role":
		return &rbacv1.Role{}
	case "rolebinding":
		return &rbacv1.RoleBinding{}
	case "service":
		return &corev1.Service{}
	case "serviceaccount":
		return &corev1.ServiceAccount{}
	default:
		ginkgo.Fail("unsupported K8S kind "+kind, 1)
		return nil
	}
}

// getModelServerPods Returns the list of Prefill and Decode vLLM pods separately
func getModelServerPods(podLabels, prefillLabels, decodeLabels map[string]string) ([]string, []string) {
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
	err := k8sClient.List(ctx, &podList, &client.ListOptions{LabelSelector: selector})
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
	var deployment appsv1.Deployment
	helper := func(deploymentName string) bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: deploymentName}, &deployment)
		return err == nil && deployment.Status.Replicas == deployment.Status.ReadyReplicas
	}
	for _, kindAndName := range objects {
		split := strings.Split(kindAndName, "/")
		if strings.ToLower(split[0]) == "deployment" {
			ginkgo.By(fmt.Sprintf("Waiting for pods of %s to be ready", split[1]))
			gomega.Eventually(helper, readyTimeout, interval).WithArguments(split[1]).Should(gomega.BeTrue())
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

// applyYAMLFile reads a file containing YAML (possibly multiple docs)
// and applies each object to the cluster.
func applyYAMLFile(filePath string) {
	// Create the resources from the manifest file
	createObjsFromYaml(readYaml(filePath))
}

func readYaml(filePath string) []string {
	ginkgo.By("Reading YAML file: " + filePath)
	yamlBytes, err := os.ReadFile(filePath)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Split multiple docs, if needed
	return strings.Split(string(yamlBytes), "\n---")
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
