# Development

Documentation for developing the inference scheduler.

## Requirements

- [Make] `v4`+
- [Golang] `v1.24`+
- [Docker] (or [Podman])
- [Kubernetes in Docker (KIND)]

[Make]:https://www.gnu.org/software/make/
[Golang]:https://go.dev/
[Docker]:https://www.docker.com/
[Podman]:https://podman.io/
[Kubernetes in Docker (KIND)]:https://github.com/kubernetes-sigs/kind

## Kind Development Environment

> **WARNING**: This current requires you to have manually built the vllm
> simulator separately on your local system. In a future iteration this will
> be handled automatically and will not be required. The tag for the simulator
> currently needs to be `0.0.4`.

You can deploy the current scheduler with a Gateway API implementation into a
[Kubernetes in Docker (KIND)] cluster locally with the following:

```console
make env-dev-kind
```

This will create a `kind` cluster (or re-use an existing one) using the system's
local container runtime and deploy the development stack into the `default`
namespace.

There are several ways to access the gateway:

**Port forward**:

```console
$ kubectl --context llm-d-inference-scheduler-dev port-forward service/inference-gateway 8080:80
```

**NodePort**

```console
# Determine the k8s node address
$ kubectl --context llm-d-inference-scheduler-dev get node -o yaml | grep address
# The service is accessible over port 80 of the worker IP address.
```

**LoadBalancer**

```console
# Install and run cloud-provider-kind:
$ go install sigs.k8s.io/cloud-provider-kind@latest && cloud-provider-kind &
$ kubectl --context llm-d-inference-scheduler-dev get service inference-gateway
# Wait for the LoadBalancer External-IP to become available. The service is accessible over port 80.
```

You can now make requests macthing the IP:port of one of the access mode above:

```console
$ curl -s -w '\n' http://<IP:port>/v1/completions -H 'Content-Type: application/json' -d '{"model":"food-review","prompt":"hi","max_tokens":10,"temperature":0}' | jq
```

By default the created inference gateway, can be accessed on port 30080. This can
be overriden to any free port in the range of 30000 to 32767, by running the above
command as follows:

```console
KIND_GATEWAY_HOST_PORT=<selected-port> make env-dev-kind
```

**Where:** &lt;selected-port&gt; is the port on your local machine you want to use to
access the inference gatyeway.

> **NOTE**: If you require significant customization of this environment beyond
> what the standard deployment provides, you can use the `deploy/components`
> with `kustomize` to build your own highly customized environment. You can use
> the `deploy/environments/kind` deployment as a reference for your own.

[Kubernetes in Docker (KIND)]:https://github.com/kubernetes-sigs/kind

### Development Cycle

To test your changes to `llm-d-inferernce-scheduler` in this environment, make your changes locally
and then re-run the deployment:

```console
make env-dev-kind
```

This will build images with your recent changes and load the new images to the
cluster. By default the image tag will be `dev`.

**NOTE:** The built image tag can be specified via the `EPP_TAG` environment variable so it is used in the deployment. For example:

```console
EPP_TAG=0.0.4 make env-dev-kind

**NOTE**: If you are working on a MacOS with Apple Silicon, it is required to add
the environment variable `GOOS=linux`.

Then do a rollout of the EPP `Deployment` so that your recent changes are
reflected:

```console
kubectl rollout restart deployment endpoint-picker
```
