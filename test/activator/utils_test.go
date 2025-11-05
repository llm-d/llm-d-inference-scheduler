package e2e

import (
	"fmt"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

// applyYAMLFile reads a file containing YAML (possibly multiple docs)
// and applies each object to the cluster.
func ApplyYAMLFile(testConfig *testutils.TestConfig, filePath string) {
	// Create the resources from the manifest file
	CreateObjsFromYaml(testConfig, testutils.ReadYaml(filePath))
}

// CreateObjsFromYaml creates K8S objects from yaml and waits for them to be instantiated
func CreateObjsFromYaml(testConfig *testutils.TestConfig, docs []string) []string {
	objNames := []string{}

	// For each doc, decode and create
	decoder := serializer.NewCodecFactory(testConfig.Scheme).UniversalDeserializer()
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
			err := testConfig.Scheme.Convert(obj, unstrObj, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		unstrObj.SetNamespace(testConfig.NsName)
		kind := unstrObj.GetKind()
		name := unstrObj.GetName()
		objNames = append(objNames, kind+"/"+name)

		// Create the object
		err := testConfig.K8sClient.Create(testConfig.Context, unstrObj, &client.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred(),
			"Failed to create object from YAML")
	}
	return objNames
}
