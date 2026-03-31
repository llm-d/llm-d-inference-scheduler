package e2e

import (
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthPb "google.golang.org/grpc/health/grpc_health_v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testutils "sigs.k8s.io/gateway-api-inference-extension/test/utils"
)

// createModelServers creates the model server resources used for testing from the given filePaths.
// Uses the default connector (nixlv2) for P/D deployments.
func createModelServers(withPD, withKV, withDP bool, vllmReplicas, prefillReplicas, decodeReplicas int) []string {
	return createModelServersWithConnector(withPD, withKV, withDP, vllmReplicas, prefillReplicas, decodeReplicas, "nixlv2")
}

// createModelServersWithConnector creates model server resources with a specific connector type.
func createModelServersWithConnector(withPD, withKV, withDP bool, vllmReplicas, prefillReplicas, decodeReplicas int, connector string) []string {
	theModelName := simModelName
	theSafeModelName := simModelName
	if withKV {
		theModelName = kvModelName
		theSafeModelName = safeKvModelName
	}
	yaml := simDeployment
	if withPD {
		yaml = simPDDisaggDeployment
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
			"${UDS_TOKENIZER_IMAGE}":  udsTokenizerImage,
		})

	objects := testutils.CreateObjsFromYaml(testConfig, manifests)
	podsInDeploymentsReady(objects)

	return objects
}

// createModelServersEpD creates model server resources for E/PD (encode + prefill/decode) testing.
func createModelServersEpD(encodeReplicas, decodeReplicas int) []string {
	manifests := testutils.ReadYaml(simEpDDisaggDeployment)
	manifests = substituteMany(manifests,
		map[string]string{
			"${MODEL_NAME}":           simModelName,
			"${MODEL_NAME_SAFE}":      simModelName,
			"${POOL_NAME}":            poolName,
			"${EC_CONNECTOR_TYPE}":    "ec-example",
			"${SIDECAR_IMAGE}":        sideCarImage,
			"${VLLM_REPLICA_COUNT_E}": strconv.Itoa(encodeReplicas),
			"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
			"${VLLM_SIMULATOR_IMAGE}": vllmSimImage,
			"${UDS_TOKENIZER_IMAGE}":  udsTokenizerImage,
		})

	objects := testutils.CreateObjsFromYaml(testConfig, manifests)
	podsInDeploymentsReady(objects)
	return objects
}

// createModelServersEPDDisagg creates model server resources for E/P/D (encode/prefill/decode) testing.
func createModelServersEPDDisagg(encodeReplicas, prefillReplicas, decodeReplicas int) []string {
	manifests := testutils.ReadYaml(simEPDDisaggDeployment)
	manifests = substituteMany(manifests,
		map[string]string{
			"${MODEL_NAME}":           simModelName,
			"${MODEL_NAME_SAFE}":      simModelName,
			"${POOL_NAME}":            poolName,
			"${KV_CONNECTOR_TYPE}":    "shared-storage",
			"${EC_CONNECTOR_TYPE}":    "ec-example",
			"${SIDECAR_IMAGE}":        sideCarImage,
			"${VLLM_REPLICA_COUNT_E}": strconv.Itoa(encodeReplicas),
			"${VLLM_REPLICA_COUNT_P}": strconv.Itoa(prefillReplicas),
			"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
			"${VLLM_SIMULATOR_IMAGE}": vllmSimImage,
			"${UDS_TOKENIZER_IMAGE}":  udsTokenizerImage,
		})

	objects := testutils.CreateObjsFromYaml(testConfig, manifests)
	podsInDeploymentsReady(objects)
	return objects
}

// createModelServersEPDUnified creates a model server resources for EPD (one pod for encode/prefill/decode) testing.
func createModelServersEPDUnified(replicas int) []string {
	manifests := testutils.ReadYaml(simEPDUnifiedDeployment)
	manifests = substituteMany(manifests,
		map[string]string{
			"${MODEL_NAME}":           simModelName,
			"${MODEL_NAME_SAFE}":      simModelName,
			"${POOL_NAME}":            poolName,
			"${VLLM_REPLICA_COUNT}":   strconv.Itoa(replicas),
			"${VLLM_SIMULATOR_IMAGE}": vllmSimImage,
			"${UDS_TOKENIZER_IMAGE}":  udsTokenizerImage,
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

	objects := make([]string, 1, 10)
	objects[0] = "ConfigMap/epp-config"

	eppYamls := testutils.ReadYaml(eppManifest)
	eppYamls = substituteMany(eppYamls,
		map[string]string{
			"${EPP_IMAGE}":           eppImage,
			"${UDS_TOKENIZER_IMAGE}": udsTokenizerImage,
			"${NAMESPACE}":           nsName,
			"${POOL_NAME}":           simModelName + "-inference-pool",
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
