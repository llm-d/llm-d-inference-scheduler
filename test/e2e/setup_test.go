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

	testutils "github.com/llm-d/llm-d-inference-scheduler/test/utils"
)

func createModelServersFromYaml(yaml string, extra map[string]string) []string {
	subs := map[string]string{
		"${MODEL_NAME}":           simModelName,
		"${MODEL_NAME_SAFE}":      simModelName,
		"${POOL_NAME}":            poolName,
		"${VLLM_SIMULATOR_IMAGE}": vllmSimImage,
		"${UDS_TOKENIZER_IMAGE}":  udsTokenizerImage,
	}
	for k, v := range extra {
		subs[k] = v
	}
	manifests := testutils.ReadYaml(yaml)
	manifests = substituteMany(manifests, subs)
	objects := testutils.CreateObjsFromYaml(testConfig, manifests)
	podsInDeploymentsReady(objects)
	return objects
}

func createModelServersDecode(replicas int) []string {
	return createModelServersFromYaml(simDeployment, map[string]string{
		"${KV_CACHE_ENABLED}":   "false",
		"${VLLM_REPLICA_COUNT}": strconv.Itoa(replicas),
	})
}

func createModelServersDecodeKV(replicas int) []string {
	return createModelServersFromYaml(simDeployment, map[string]string{
		"${MODEL_NAME}":         kvModelName,
		"${MODEL_NAME_SAFE}":    safeKvModelName,
		"${KV_CACHE_ENABLED}":   "true",
		"${VLLM_REPLICA_COUNT}": strconv.Itoa(replicas),
	})
}

func createModelServersDecodeDP(replicas int) []string {
	return createModelServersFromYaml(simDPDeployment, map[string]string{
		"${SIDECAR_IMAGE}":      sideCarImage,
		"${VLLM_REPLICA_COUNT}": strconv.Itoa(replicas),
	})
}

func createModelServersPDWithConnector(prefillReplicas, decodeReplicas int, connector string) []string {
	return createModelServersFromYaml(simPDDisaggDeployment, map[string]string{
		"${KV_CACHE_ENABLED}":     "false",
		"${CONNECTOR_TYPE}":       connector,
		"${SIDECAR_IMAGE}":        sideCarImage,
		"${VLLM_REPLICA_COUNT}":   "0",
		"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
		"${VLLM_REPLICA_COUNT_P}": strconv.Itoa(prefillReplicas),
	})
}

func createModelServersPDNixl(prefillReplicas, decodeReplicas int) []string {
	return createModelServersPDWithConnector(prefillReplicas, decodeReplicas, "nixlv2")
}

func createModelServersPDSharedStorage(decodeReplicas int) []string {
	return createModelServersPDWithConnector(1, decodeReplicas, "shared-storage")
}

// createModelServersEpDDisagg creates model server resources for E/PD (encode + prefill/decode) testing.
func createModelServersEpDDisagg(encodeReplicas, decodeReplicas int) []string {
	return createModelServersFromYaml(simEpDDisaggDeployment, map[string]string{
		"${EC_CONNECTOR_TYPE}":    "ec-example",
		"${SIDECAR_IMAGE}":        sideCarImage,
		"${VLLM_REPLICA_COUNT_E}": strconv.Itoa(encodeReplicas),
		"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
	})
}

// createModelServersEPDDisagg creates model server resources for E/P/D (encode/prefill/decode) testing.
func createModelServersEPDDisagg(encodeReplicas, prefillReplicas, decodeReplicas int) []string {
	return createModelServersFromYaml(simEPDDisaggDeployment, map[string]string{
		"${KV_CONNECTOR_TYPE}":    "shared-storage",
		"${EC_CONNECTOR_TYPE}":    "ec-example",
		"${SIDECAR_IMAGE}":        sideCarImage,
		"${VLLM_REPLICA_COUNT_E}": strconv.Itoa(encodeReplicas),
		"${VLLM_REPLICA_COUNT_P}": strconv.Itoa(prefillReplicas),
		"${VLLM_REPLICA_COUNT_D}": strconv.Itoa(decodeReplicas),
	})
}

// createModelServersEPDUnified creates model server resources for EPD (one deployment for encode/prefill/decode) testing.
func createModelServersEPDUnified(replicas int) []string {
	return createModelServersFromYaml(simEPDUnifiedDeployment, map[string]string{
		"${VLLM_REPLICA_COUNT}": strconv.Itoa(replicas),
	})
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
