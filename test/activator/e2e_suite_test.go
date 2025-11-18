package e2e

import (
	"fmt"
	"io"
	"os/exec"
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

	istiov1 "istio.io/client-go/pkg/apis/networking/v1"
	istiov1a3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	gtwv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// gatewayCrdsKustomize is the manifest for the gateway api
	gatewayCrdsKustomize = "../../deploy/components/crds-gateway-api"
	// gieCrdsKustomize is the manifest for the inference pool CRD with 'inference.networking.x-k8s.io' group.
	gieCrdsKustomize = "../../deploy/components/crds-gie"
	// inferExtManifest is the manifest for the inference extension test resources.
	inferExtManifest = "./yaml/inference-pools.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	eppManifest = "./yaml/epp.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	eppConfigManifest = "./yaml/epp-configmap.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	activatorManifest = "./yaml/activator.yaml"
	// eppManifest is the manifest for the deployment of the EPP
	activatorfilterManifest = "./yaml/activator-filters.yaml"
	// rbacManifest is the manifest for the EPP's RBAC resources.
	rbacManifest = "./yaml/rbacs.yaml"
	// serviceAccountManifest is the manifest for the EPP's service account resources.
	serviceAccountManifest = "./yaml/service-accounts.yaml"
	// servicesManifest is the manifest for the EPP's service resources.
	servicesManifest = "./yaml/services.yaml"
	// nsName is the namespace in which the K8S objects will be created
	networkConfigurationManifest = "./yaml/network-config.yaml"
)

var (
	port       string
	testConfig *testutils.TestConfig

	eppImg       = env.GetEnvString("EPP_IMAGE", "llm-d-inference-scheduler", ginkgo.GinkgoLogr)
	eppTag       = env.GetEnvString("EPP_TAG", "dev", ginkgo.GinkgoLogr)
	activatorImg = env.GetEnvString("ACTIVATOR_IMAGE", "llm-d-activator", ginkgo.GinkgoLogr)
	activatorTag = env.GetEnvString("ACTIVATOR_TAG", "dev", ginkgo.GinkgoLogr)
	vllmImg      = env.GetEnvString("VLLM_IMAGE", "llm-d-inference-sim", ginkgo.GinkgoLogr)
	vllmTag      = env.GetEnvString("VLLM_TAG", "dev", ginkgo.GinkgoLogr)

	imageRegistry = env.GetEnvString("IMAGE_REGISTRY", "ghcr.io/llm-d", ginkgo.GinkgoLogr)
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
	testConfig = testutils.NewTestConfig("default")
	setupK8sClient()
	createCRDs(gieCrdsKustomize)
	createCRDs(gatewayCrdsKustomize)
	createIstio()
	createResources()
	loadImages()
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
}

func createResources() {
	ApplyYAMLFile(testConfig, rbacManifest)
	ApplyYAMLFile(testConfig, servicesManifest)
	ApplyYAMLFile(testConfig, eppConfigManifest)
	ApplyYAMLFile(testConfig, serviceAccountManifest)
	ApplyYAMLFile(testConfig, activatorfilterManifest)
	ApplyYAMLFile(testConfig, networkConfigurationManifest)
}

func loadImages() {
	kindLoadImage(imageRegistry + "/" + eppImg + ":" + eppTag)
	kindLoadImage(imageRegistry + "/" + vllmImg + ":" + vllmTag)
	kindLoadImage(imageRegistry + "/" + activatorImg + ":" + activatorTag)
}

func kindLoadImage(image string) {
	ginkgo.By(fmt.Sprintf("Loading %s into the cluster e2e-tests", image))

	command := exec.Command("kind", "--name", "e2e-tests", "load", "docker-image", image)
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
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

	err = gtwv1.Install(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = istiov1.AddToScheme(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = istiov1a3.AddToScheme(testConfig.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	testConfig.CreateCli()

	k8slog.SetLogger(ginkgo.GinkgoLogr)
}

// createCRDs creates the Inference Extension CRDs used for testing.
func createCRDs(manifests string) {
	crds := runKustomize(manifests)
	CreateObjsFromYaml(testConfig, crds)
}

func runKustomize(kustomizeDir string) []string {
	command := exec.Command("kustomize", "build", kustomizeDir)
	session, err := gexec.Start(command, nil, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
	return strings.Split(string(session.Out.Contents()), "\n---")
}

func createIstio() {
	command := exec.Command("helmfile", "apply", "-f", "./yaml/istio.helmfile.yaml")
	session, err := gexec.Start(command, ginkgo.GinkgoWriter, ginkgo.GinkgoWriter)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Eventually(session).WithTimeout(600 * time.Second).Should(gexec.Exit(0))
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
