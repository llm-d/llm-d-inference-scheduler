package e2e

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	k8slog "sigs.k8s.io/controller-runtime/pkg/log"

	infextv1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	infextv1a2 "sigs.k8s.io/gateway-api-inference-extension/apix/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

const (
	// defaultReadyTimeout is the default timeout for a resource to report a ready state.
	defaultReadyTimeout = 3 * time.Minute
	// defaultInterval is the default interval to check if a resource exists or ready conditions.
	defaultInterval = time.Millisecond * 250
	// xInferPoolManifest is the manifest for the inference pool CRD with 'inference.networking.x-k8s.io' group.
	gieCrdsKustomize = "../../deploy/components/crds-gie"
	// inferExtManifest is the manifest for the inference extension test resources.
	inferExtManifest = "./yaml/inference-pools.yaml"
	// modelName is the test model name.
	modelName = "food-review"
	// kvModelName is the model name used in KV tests.
	kvModelName = "Qwen/Qwen2.5-1.5B-Instruct"
	// safeKvModelName is the safe form of the model name used in KV tests
	safeKvModelName = "qwen-qwen2-5-1-5b-instruct"
	// envoyManifest is the manifest for the envoy proxy test resources.
	envoyManifest = "./yaml/envoy.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	eppManifest = "./yaml/deployments.yaml"
	// rbacManifest is the manifest for the EPP's RBAC resources.
	rbacManifest = "./yaml/rbac.yaml"
	// serviceAccountManifest is the manifest for the EPP's service account resources.
	serviceAccountManifest = "./yaml/service-accounts.yaml"
	// servicesManifest is the manifest for the EPP's service resources.
	servicesManifest = "./yaml/services.yaml"
)

var (
	port string = env.GetEnvString("E2E_PORT", "30080", ginkgo.GinkgoLogr)

	testConfig *testutils.TestConfig

	containerRuntime = env.GetEnvString("CONTAINER_RUNTIME", "docker", ginkgo.GinkgoLogr)
	eppImage         = env.GetEnvString("EPP_IMAGE", "ghcr.io/llm-d/llm-d-inference-scheduler:dev", ginkgo.GinkgoLogr)
	vllmSimImage     = env.GetEnvString("VLLM_SIMULATOR_IMAGE", "ghcr.io/llm-d/llm-d-inference-sim:dev", ginkgo.GinkgoLogr)
	sideCarImage     = env.GetEnvString("SIDECAR_IMAGE", "ghcr.io/llm-d/llm-d-routing-sidecar:dev", ginkgo.GinkgoLogr)

	// nsName is the namespace in which the K8S objects will be created
	nsName = env.GetEnvString("NAMESPACE", "default", ginkgo.GinkgoLogr)

	// k8sContext is the Kubernetes context to work with
	k8sContext = env.GetEnvString("K8S_CONTEXT", "", ginkgo.GinkgoLogr)

	readyTimeout = env.GetEnvDuration("READY_TIMEOUT", defaultReadyTimeout, ginkgo.GinkgoLogr)
	interval     = defaultInterval

	crdObjects            []string
	envoyObjects          []string
	rbacObjects           []string
	serviceAccountObjects []string
	serviceObjects        []string
	infPoolObjects        []string
	createdNameSpace      bool

	portForwardSession *gexec.Session
)

func TestEndToEnd(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		"End To End Test Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	if k8sContext == "" {
		setupK8sCluster()
	}
	testConfig = testutils.NewTestConfig(nsName, k8sContext)
	setupK8sClient()
	setupNameSpace()
	createCRDs()
	createEnvoy()
	rbacObjects = testutils.ApplyYAMLFile(testConfig, rbacManifest)
	serviceAccountObjects = testutils.ApplyYAMLFile(testConfig, serviceAccountManifest)
	serviceObjects = testutils.ApplyYAMLFile(testConfig, servicesManifest)

	// Prevent failure in tests due to InferencePool not existing before the test
	infPoolObjects = createInferencePool(1, false)
})

var _ = ginkgo.AfterSuite(func() {
	if k8sContext != "" {
		// Used an existing Kubernetes context
		// Stop port-forward
		if portForwardSession != nil {
			portForwardSession.Terminate()
		}

		// cleanup created objects
		ginkgo.By("Deleting created Kubernetes objects")
		testutils.DeleteObjects(testConfig, infPoolObjects)
		testutils.DeleteObjects(testConfig, serviceObjects)
		testutils.DeleteObjects(testConfig, serviceAccountObjects)
		testutils.DeleteObjects(testConfig, rbacObjects)
		testutils.DeleteObjects(testConfig, envoyObjects)
		testutils.DeleteObjects(testConfig, crdObjects)

		if createdNameSpace {
			ginkgo.By("Deleting namespace " + nsName)
			err := testConfig.KubeCli.CoreV1().Namespaces().Delete(testConfig.Context, nsName, metav1.DeleteOptions{})
			gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		}
		return
	}

	command := exec.Command("kind", "delete", "cluster", "--name", "e2e-tests")
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
})

// Create the Kubernetes cluster for the E2E tests and load the local images
func setupK8sCluster() {
	command := exec.Command("kind", "create", "cluster", "--name", "e2e-tests", "--config", "-")
	stdin, err := command.StdinPipe()
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	go func() {
		defer func() {
			err := stdin.Close()
			gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		}()
		clusterConfig := strings.ReplaceAll(kindClusterConfig, "${PORT}", port)
		_, err := io.WriteString(stdin, clusterConfig)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	}()
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	kindLoadImage(vllmSimImage)
	kindLoadImage(eppImage)
	kindLoadImage(sideCarImage)
}

func kindLoadImage(image string) {
	tempDir := ginkgo.GinkgoT().TempDir()
	target := tempDir + "/container.tar"

	ginkgo.By(fmt.Sprintf("Loading %s into the cluster e2e-tests using %s", image, containerRuntime))

	_, err := exec.LookPath(containerRuntime)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "Could not find %s in PATH", containerRuntime)

	saveArgs := []string{"save", "--output", target}
	if containerRuntime == "docker" {
		// The platform flag is required for docker save to work but it is an unsupported flag for podman
		saveArgs = append(saveArgs, "--platform", "linux/"+runtime.GOARCH)
	}
	saveArgs = append(saveArgs, image)

	command := exec.Command(containerRuntime, saveArgs...)
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	command = exec.Command("kind", "--name", "e2e-tests", "load", "image-archive", target)
	session, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
}

func setupK8sClient() {
	k8sCfg, err := config.GetConfigWithContext(k8sContext)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.ExpectWithOffset(1, k8sCfg).NotTo(gomega.BeNil())

	err = clientgoscheme.AddToScheme(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = infextv1.Install(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = apiextv1.AddToScheme(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = infextv1a2.Install(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	testConfig.CreateCli()

	k8slog.SetLogger(ginkgo.GinkgoLogr)
}

// setupNameSpace sets up the specified namespace if it doesn't exist
func setupNameSpace() {
	if nsName == "default" {
		return
	}
	_, err := testConfig.KubeCli.CoreV1().Namespaces().Get(testConfig.Context, nsName, metav1.GetOptions{})
	if err == nil {
		return
	}
	gomega.Expect(errors.IsNotFound(err)).To(gomega.BeTrue())

	ginkgo.By("Creating namespace " + nsName)
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
		},
	}
	_, err = testConfig.KubeCli.CoreV1().Namespaces().Create(testConfig.Context, namespace, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	createdNameSpace = true
}

// createCRDs creates the Inference Extension CRDs used for testing.
func createCRDs() {
	crds := runKustomize(gieCrdsKustomize)
	crdObjects = testutils.CreateObjsFromYaml(testConfig, crds)
}

func createEnvoy() {
	manifests := testutils.ReadYaml(envoyManifest)
	manifests = substituteMany(manifests, map[string]string{"${NAMESPACE}": nsName})
	ginkgo.By("Creating envoy proxy resources from manifest: " + envoyManifest)
	envoyObjects = testutils.CreateObjsFromYaml(testConfig, manifests)

	if k8sContext != "" {
		envoyName := ""
		for _, obj := range envoyObjects {
			splitObj := strings.Split(obj, "/")
			if strings.ToLower(splitObj[0]) == "deployment" {
				envoyName = splitObj[1]
			}
		}
		gomega.Expect(envoyName).ToNot(gomega.BeEmpty())

		command := exec.Command("kubectl", "port-forward", "deployment/"+envoyName, port+":8081",
			"--context="+k8sContext, "--namespace="+nsName)
		var err error
		portForwardSession, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	}
}

func createInferencePool(numTargetPorts int, toDelete bool) []string {
	poolName := modelName + "-inference-pool"

	if toDelete {
		objName := []string{"inferencepool/" + poolName}
		testutils.DeleteObjects(testConfig, objName)
	}

	infPoolYaml := testutils.ReadYaml(inferExtManifest)
	targetPorts := ""
	for idx := range numTargetPorts {
		targetPorts += fmt.Sprintf("\n  - number: %d", 8000+idx)
	}
	infPoolYaml = substituteMany(infPoolYaml,
		map[string]string{
			"${POOL_NAME}":    poolName,
			"${TARGET_PORTS}": targetPorts,
		})

	return testutils.CreateObjsFromYaml(testConfig, infPoolYaml)
}

const kindClusterConfig = `
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- extraPortMappings:
  - containerPort: 30080
    hostPort: ${PORT}
    protocol: TCP
  - containerPort: 30081
    hostPort: 30081
    protocol: TCP
`
