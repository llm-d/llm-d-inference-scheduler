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
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
	// nsName is the namespace in which the K8S objects will be created
	nsName = "default"
)

var (
	port string

	testConfig *testutils.TestConfig

	eppTag            = env.GetEnvString("EPP_TAG", "dev", ginkgo.GinkgoLogr)
	vllmSimTag        = env.GetEnvString("VLLM_SIMULATOR_TAG", "dev", ginkgo.GinkgoLogr)
	routingSideCarTag = env.GetEnvString("SIDECAR_TAG", "dev", ginkgo.GinkgoLogr)

	readyTimeout = env.GetEnvDuration("READY_TIMEOUT", defaultReadyTimeout, ginkgo.GinkgoLogr)
	interval     = defaultInterval
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
	testConfig = testutils.NewTestConfig(nsName)
	setupK8sClient()
	createCRDs()
	createEnvoy()
	testutils.ApplyYAMLFile(testConfig, rbacManifest)
	testutils.ApplyYAMLFile(testConfig, serviceAccountManifest)
	testutils.ApplyYAMLFile(testConfig, servicesManifest)

	infPoolYaml := testutils.ReadYaml(inferExtManifest)
	infPoolYaml = substituteMany(infPoolYaml, map[string]string{"${POOL_NAME}": modelName + "-inference-pool"})
	testutils.CreateObjsFromYaml(testConfig, infPoolYaml)
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

	kindLoadImage("ghcr.io/llm-d/llm-d-inference-sim:" + vllmSimTag)
	kindLoadImage("ghcr.io/llm-d/llm-d-inference-scheduler:" + eppTag)
	kindLoadImage("ghcr.io/llm-d/llm-d-routing-sidecar:" + routingSideCarTag)
}

func kindLoadImage(image string) {
	tempDir := ginkgo.GinkgoT().TempDir()
	target := tempDir + "/docker.tar"

	ginkgo.By(fmt.Sprintf("Loading %s into the cluster e2e-tests", image))

	command := exec.Command("docker", "save", "--platform", "linux/"+runtime.GOARCH,
		"--output", target, image)
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))

	command = exec.Command("kind", "--name", "e2e-tests", "load", "image-archive", target)
	session, err = gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
}

func setupK8sClient() {
	k8sCfg := config.GetConfigOrDie()
	gomega.ExpectWithOffset(1, k8sCfg).NotTo(gomega.BeNil())

	err := clientgoscheme.AddToScheme(testConfig.Scheme)
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

// createCRDs creates the Inference Extension CRDs used for testing.
func createCRDs() {
	crds := runKustomize(gieCrdsKustomize)
	testutils.CreateObjsFromYaml(testConfig, crds)
}

func createEnvoy() {
	manifests := testutils.ReadYaml(envoyManifest)
	ginkgo.By("Creating envoy proxy resources from manifest: " + envoyManifest)
	testutils.CreateObjsFromYaml(testConfig, manifests)
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
