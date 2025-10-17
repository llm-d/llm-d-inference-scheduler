package e2e

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	k8slog "sigs.k8s.io/controller-runtime/pkg/log"
	infextv1a2 "sigs.k8s.io/gateway-api-inference-extension/apix/v1alpha2"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/env"
)

const (
	// defaultExistsTimeout is the default timeout for a resource to exist in the api server.
	defaultExistsTimeout = 30 * time.Second
	// defaultReadyTimeout is the default timeout for a resource to report a ready state.
	defaultReadyTimeout = 3 * time.Minute
	// defaultModelReadyTimeout is the default timeout for the model server deployment to report a ready state.
	defaultModelReadyTimeout = 10 * time.Minute
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
	// nsName is the namespace in which the K8S objects will be created
	nsName = "default"
)

var (
	ctx       = context.Background()
	k8sClient client.Client
	port      string
	scheme    = runtime.NewScheme()

	eppImage            = env.GetEnvString("EPP_IMAGE", "ghcr.io/llm-d/llm-d-inference-scheduler:dev", ginkgo.GinkgoLogr)
	vllmSimImage        = env.GetEnvString("VLLM_SIMULATOR_IMAGE", "ghcr.io/llm-d/llm-d-inference-sim:dev", ginkgo.GinkgoLogr)
	routingSideCarImage = env.GetEnvString("ROUTING_SIDECAR_IMAGE", "ghcr.io/llm-d/llm-d-routing-sidecar:v0.2.0", ginkgo.GinkgoLogr)

	existsTimeout     = env.GetEnvDuration("EXISTS_TIMEOUT", defaultExistsTimeout, ginkgo.GinkgoLogr)
	readyTimeout      = env.GetEnvDuration("READY_TIMEOUT", defaultReadyTimeout, ginkgo.GinkgoLogr)
	modelReadyTimeout = env.GetEnvDuration("MODEL_READY_TIMEOUT", defaultModelReadyTimeout, ginkgo.GinkgoLogr)
	interval          = defaultInterval
)

func TestEndToEnd(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t,
		"End To End Test Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	port = "30080"

	setupK8sCluster()
	setupK8sClient()
	createCRDs()
	createEnvoy()
	applyYAMLFile(rbacManifest)
	applyYAMLFile(serviceAccountManifest)
	applyYAMLFile(servicesManifest)

	infPoolYaml := readYaml(inferExtManifest)
	infPoolYaml = substituteMany(infPoolYaml, map[string]string{"${POOL_NAME}": modelName + "-inference-pool"})
	createObjsFromYaml(infPoolYaml)
})

var _ = ginkgo.AfterSuite(func() {
	command := exec.Command("kind", "delete", "cluster", "--name", "e2e-tests")
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
})

// loadImageIntoKind loads the specified image
// into the Kind cluster using the most appropriate method based on the container runtime.
func loadImageIntoKind(imageName string) {
	container_runtime := env.GetEnvString("CONTAINER_TOOL", "docker", ginkgo.GinkgoLogr)
	ginkgo.By("Loading image into Kind cluster: " + imageName)

	switch container_runtime {
	case "podman":
		// Detect if podman is available
		podmanPath, podmanErr := exec.LookPath("podman")
		gomega.Expect(podmanErr).ShouldNot(gomega.HaveOccurred(), "Could not find podman in PATH")
		ginkgo.GinkgoLogr.Info("Podman detected, using image-archive method.", "path", podmanPath)

		// Create a temporary file to hold the image archive.
		tmpFile, err := os.CreateTemp("", "image-archive-*.tar")
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		// Ensure the temporary file is cleaned up when the function exits.
		defer os.Remove(tmpFile.Name())

		// Save the image to the temp file
		cmdPodmanSave := exec.Command("podman", "save", "-o", tmpFile.Name(), imageName)
		saveSession, err := gexec.Start(cmdPodmanSave, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Eventually(saveSession).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

		// Load the temp file image into kind
		cmdKindLoad := exec.Command("kind", "load", "image-archive", "--name", "e2e-tests", tmpFile.Name())
		loadSession, err := gexec.Start(cmdKindLoad, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Eventually(loadSession).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
	case "docker":
		// Detect if podman is available
		dockerPath, dockerErr := exec.LookPath("docker")
		gomega.Expect(dockerErr).ShouldNot(gomega.HaveOccurred(), "Could not find docker in PATH")

		ginkgo.GinkgoLogr.Info("Docker detected, using docker-image method.", "path", dockerPath)
		command := exec.Command("kind", "load", "docker-image", "--name", "e2e-tests", imageName)
		session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
	default:
		ginkgo.Fail("ERROR: Could not find 'podman' or 'docker' in the system's PATH. Please install one to continue.")
	}
}

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

	loadImageIntoKind(vllmSimImage)
	loadImageIntoKind(eppImage)
	loadImageIntoKind(routingSideCarImage)
}

func setupK8sClient() {
	k8sCfg := config.GetConfigOrDie()
	gomega.ExpectWithOffset(1, k8sCfg).NotTo(gomega.BeNil())

	err := clientgoscheme.AddToScheme(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = apiextv1.AddToScheme(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = infextv1a2.Install(scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	k8sClient, err = client.New(k8sCfg, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	k8slog.SetLogger(ginkgo.GinkgoLogr)
}

// createCRDs creates the Inference Extension CRDs used for testing.
func createCRDs() {
	crds := runKustomize(gieCrdsKustomize)
	createObjsFromYaml(crds)
}

func createEnvoy() {
	manifests := readYaml(envoyManifest)
	ginkgo.By("Creating envoy proxy resources from manifest: " + envoyManifest)
	createObjsFromYaml(manifests)
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
