# Development

Documentation for developing the inference scheduler.

## Requirements

- [Make] `v4`+
- [Golang] `v1.24`+
- [Docker] (or [Podman])
- [Kubernetes in Docker (KIND)]
- [Kustomize]

[Make]:https://www.gnu.org/software/make/
[Golang]:https://go.dev/
[Docker]:https://www.docker.com/
[Podman]:https://podman.io/
[Kubernetes in Docker (KIND)]:https://github.com/kubernetes-sigs/kind
[Kustomize]:https://kubectl.docs.kubernetes.io/installation/kustomize/

## Kind Development Environment

The following deployment creates a [Kubernetes in Docker (KIND)] cluster with an inference scheduler using a Gateway API implementation, connected to the vLLM simulator.
To run the deployment, use the following command:

```bash
make env-dev-kind
```

This will create a `kind` cluster (or re-use an existing one) using the system's
local container runtime and deploy the development stack into the `default`
namespace.

> [!NOTE]
> You can download the image locally using `docker pull ghcr.io/llm-d/llm-d-inference-sim:latest`, and the script will load it from your local Docker registry.

There are several ways to access the gateway:

**Port forward**:

```bash
$ kubectl --context llm-d-inference-scheduler-dev port-forward service/inference-gateway 8080:80
```

**NodePort**

```bash
# Determine the k8s node address
$ kubectl --context llm-d-inference-scheduler-dev get node -o yaml | grep address
# The service is accessible over port 80 of the worker IP address.
```

**LoadBalancer**

```bash
# Install and run cloud-provider-kind:
$ go install sigs.k8s.io/cloud-provider-kind@latest && cloud-provider-kind &
$ kubectl --context llm-d-inference-scheduler-dev get service inference-gateway
# Wait for the LoadBalancer External-IP to become available. The service is accessible over port 80.
```

You can now make requests matching the IP:port of one of the access mode above:

```bash
$ curl -s -w '\n' http://<IP:port>/v1/completions -H 'Content-Type: application/json' -d '{"model":"food-review","prompt":"hi","max_tokens":10,"temperature":0}' | jq
```

By default the created inference gateway, can be accessed on port 30080. This can
be overridden to any free port in the range of 30000 to 32767, by running the above
command as follows:

```bash
KIND_GATEWAY_HOST_PORT=<selected-port> make env-dev-kind
```

**Where:** &lt;selected-port&gt; is the port on your local machine you want to use to
access the inference gatyeway.

> [!NOTE]
> If you require significant customization of this environment beyond
> what the standard deployment provides, you can use the `deploy/components`
> with `kustomize` to build your own highly customized environment. You can use
> the `deploy/environments/kind` deployment as a reference for your own.

[Kubernetes in Docker (KIND)]:https://github.com/kubernetes-sigs/kind

### Development Cycle

To test your changes to `llm-d-inference-scheduler` in this environment, make your changes locally
and then re-run the deployment:

```bash
make env-dev-kind
```

This will build images with your recent changes and load the new images to the
cluster. By default the image tag will be `dev`. It will also load `llm-d-inference-sim` image.

> [!NOTE]
>The built image tag can be specified via the `EPP_TAG` environment variable so it is used in the deployment. For example:

```bash
EPP_TAG=0.0.4 make env-dev-kind
```

> [!NOTE]
> If you want to load a different tag of llm-d-inference-sim, you can use the environment variable `VLLM_SIMULATOR_TAG` to specify it.

> [!NOTE]
> If you are working on a MacOS with Apple Silicon, it is required to add the environment variable `GOOS=linux`.

Then do a rollout of the EPP `Deployment` so that your recent changes are
reflected:

```bash
kubectl rollout restart deployment food-review-endpoint-picker
```

## Kubernetes Development Environment

A Kubernetes cluster can be used for development and testing.
The setup can be split in two:

- cluster-level infrastructure deployment (e.g., CRDs), and
- deployment of development environments on a per-namespace basis

This enables cluster sharing by multiple developers. In case of private/personal
clusters, the `default` namespace can be used directly.

### Setup - Infrastructure

> [!CAUTION]
> In shared cluster situations you should probably not be
> running this unless you're the cluster admin and you're _certain_
> that you should be running this, as this can be disruptive to other developers
> in the cluster.

The following will deploy all the infrastructure-level requirements (e.g. CRDs,
Operators, etc.) to support the namespace-level development environments:

Install Gateway API + GIE CRDs:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.3.0/standard-install.yaml
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/latest/download/manifests.yaml
```

Install kgateway:
```bash
KGTW_VERSION=v2.0.2
helm upgrade -i --create-namespace --namespace kgateway-system --version $KGTW_VERSION kgateway-crds oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds
helm upgrade -i --namespace kgateway-system --version $KGTW_VERSION kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway --set inferenceExtension.enabled=true
```

For more details, see the Gateway API inference Extension [getting started guide](https://gateway-api-inference-extension.sigs.k8s.io/guides/)

### Setup - Developer Environment

> [!NOTE]
> This setup is currently very manual in regards to container
> images for the VLLM simulator and the EPP. It is expected that you build and
> push images for both to your own private registry. In future iterations, we
> will be providing automation around this to make it simpler.

To deploy a development environment to the cluster, you'll need to explicitly
provide a namespace. This can be `default` if this is your personal cluster,
but on a shared cluster you should pick something unique. For example:

```bash
export NAMESPACE=annas-dev-environment
```

Create the namespace:

```bash
kubectl create namespace ${NAMESPACE}
```

Set the default namespace for kubectl commands

```bash
kubectl config set-context --current --namespace="${NAMESPACE}"
```

> [!NOTE]
> If you are using OpenShift (oc CLI), you can use the following instead: `oc project "${NAMESPACE}"`

- Set Hugging Face token variable:

```bash
export HF_TOKEN="<HF_TOKEN>"
```

Download the `llm-d-kv-cache-manager` repository (the installation script and Helm chart to install the vLLM environment):

```bash
cd .. && git clone git@github.com:llm-d/llm-d-kv-cache-manager.git
```

If you prefer to clone it into the `/tmp` directory, make sure to update the `VLLM_CHART_DIR` environment variable:
`export VLLM_CHART_DIR=<tmp_dir>/llm-d-kv-cache-manager/vllm-setup-helm`

Once all this is set up, you can deploy the environment:

```bash
make env-dev-kubernetes
```

This will deploy the entire stack to whatever namespace you chose.
> [!NOTE]
> The model and images of each component can  be replaced. See [Environment Configuration](#environment-configuration) for model settings.

You can test by exposing the `inference gateway` via port-forward:

```bash
kubectl port-forward service/inference-gateway 8080:80 -n "${NAMESPACE}"
```

And making requests with `curl`:

```bash
curl -s -w '\n' http://localhost:8080/v1/completions -H 'Content-Type: application/json' \
  -d '{"model":"meta-llama/Llama-3.1-8B-Instruct","prompt":"hi","max_tokens":10,"temperature":0}' | jq
```

> [!NOTE]
> If the response is empty or contains an error, jq may output a cryptic error. You can run the command without jq to debug raw responses.

#### Environment Configurateion

**1. Setting the EPP image and tag:**

You can optionally set a custom EPP image (otherwise, the default will be used):

```bash
export EPP_TAG="<YOUR_TAG>"
export EPP_IMAGE="<YOUR_REGISTRY>/<YOUR_IMAGE>"
```

**2. Setting the vLLM replicas:**

You can optionally set the vllm replicas:

```bash
export VLLM_REPLICA_COUNT=2
```

**3. Setting the model name:**

You can replace the model name that will be used in the system.

```bash
export MODEL_NAME=mistralai/Mistral-7B-Instruct-v0.2
```

If you need to deploy a larger model, update the vLLM-related parameters according to the model's requirements. For example:

```bash
export MODEL_NAME=meta-llama/Llama-3.1-70B-Instruct
export PVC_SIZE=200Gi
export VLLM_MEMORY_RESOURCES=100Gi
export VLLM_GPU_MEMORY_UTILIZATION=0.95
export VLLM_TENSOR_PARALLEL_SIZE=2
export VLLM_GPU_COUNT_PER_INSTANCE=2
```

**4. Additional environment settings:**

More environment variable settings can be found in the `scripts/kubernetes-dev-env.sh`.

#### Development Cycle

> [!Warning]
> This is a very manual process at the moment. We expect to make
> this more automated in future iterations.

Make your changes locally and commit them. Then select an image tag based on
the `git` SHA and set your private registry:

```bash
export EPP_TAG=$(git rev-parse HEAD)
export IMAGE_REGISTRY="quay.io/<my-id>"
```

Build the image and tag the image for your private registry:

```bash
make image-build
```

and push it:

```bash
make image-push
```

You can now re-deploy the environment with your changes (don't forget all of
the required environment variables):

```bash
make env-dev-kubernetes
```

And test the changes.

### Cleanup Environment

To clean up the development environment and remove all deployed resources in your namespace, run:

```bash
make clean-env-dev-kubernetes
```

If you also want to remove the namespace entirely, run:

```bash
kubectl delete namespace ${NAMESPACE}
```

To uninstall the infra-stracture development:
Uninstal GIE CRDs:

```bash
kubectl delete -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/latest/download/manifests.yaml --ignore-not-found
```

Uninstall kgateway:

```bash
helm uninstall kgateway -n kgateway-system
helm uninstall kgateway-crds -n kgateway-system
```

For more details, see the Gateway API inference Extension [getting started guide](https://gateway-api-inference-extension.sigs.k8s.io/guides/)
