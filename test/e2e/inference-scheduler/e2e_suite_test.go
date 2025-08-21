package inferencescheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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

	infextv1a2 "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"
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
	gieCrdsKustomize = "../../../deploy/components/crds-gie"
	// inferExtManifest is the manifest for the inference extension test resources.
	inferExtManifest = "../yaml/inference-pools.yaml"
	// modelName is the test model name.
	modelName = "food-review"
	// kvModelName is the model name used in KV tests.
	kvModelName = "Qwen/Qwen2.5-1.5B-Instruct"
	// safeKvModelName is the safe fom of the model name used in KV tests
	safeKvModelName = "qwen-qwen2-5-1-5b-instruct"
	// envoyManifest is the manifest for the envoy proxy test resources.
	envoyManifest = "../yaml/envoy.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	eppManifest = "../yaml/deployments.yaml"
	// rbacManifest is the manifest for the EPP's RBAC resources.
	rbacManifest = "../yaml/rbac.yaml"
	// serviceAccountManifest is the manifest for the EPP's service account resources.
	serviceAccountManifest = "../yaml/service-accounts.yaml"
	// servicesManifest is the manifest for the EPP's service resources.
	servicesManifest = "../yaml/services.yaml"
	// nsName is the namespace in which the K8S objects will be created
	nsName = "default"
)

var (
	ctx       = context.Background()
	k8sClient client.Client
	port      string
	scheme    = runtime.NewScheme()

	vllmSimTag        = getEnvWithDefault("VLLM_SIMULATOR_TAG", "dev")
	routingSideCarTag = getEnvWithDefault("ROUTING_SIDECAR_TAG", "v0.2.0")

	existsTimeout     = getTimeout("EXISTS_TIMEOUT", defaultExistsTimeout)
	readyTimeout      = getTimeout("READY_TIMEOUT", defaultReadyTimeout)
	modelReadyTimeout = getTimeout("MODEL_READY_TIMEOUT", defaultModelReadyTimeout)
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

// Create the Kubernetes cluster for the E2E tests and load the local images
func setupK8sCluster() {
	command := exec.Command("kind", "create", "cluster", "--name", "e2e-tests", "--config", "-")
	stdin, err := command.StdinPipe()
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	go func() {
		defer stdin.Close()
		clusterConfig := strings.ReplaceAll(kindClusterConfig, "${PORT}", port)
		io.WriteString(stdin, clusterConfig)
	}()
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	command = exec.Command("kind", "--name", "e2e-tests", "load", "docker-image",
		fmt.Sprintf("ghcr.io/llm-d/llm-d-inference-sim:%s", vllmSimTag))
	session, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	command = exec.Command("kind", "--name", "e2e-tests", "load", "docker-image",
		"ghcr.io/llm-d/llm-d-inference-scheduler:dev")
	session, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	command = exec.Command("kind", "--name", "e2e-tests", "load", "docker-image",
		"ghcr.io/llm-d/llm-d-routing-sidecar:v0.2.0")
	session, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
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

func getFreePort() (string, error) {
	listener, err := net.Listen("tcp4", ":0")
	if err != nil {
		return "", err
	}

	address := listener.Addr().String()
	portIdx := strings.Index(address, ":")
	if portIdx == -1 {
		return "", errors.New("failed to find free port")
	}
	return address[portIdx+1:], nil
}

func getTimeout(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvWithDefault(envName string, defaultValue string) string {
	result := os.Getenv(envName)
	if result != "" {
		return result
	}
	return defaultValue
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
