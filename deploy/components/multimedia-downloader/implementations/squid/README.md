# Squid Proxy: Multimedia Cache

This Squid proxy implementation caches multimedia content (like images and videos) to optimize data retrieval speeds.

## 🚀 Automated Testing

Execute the [test-squid.sh](test/test-squid.sh) script to run cache validations and inspect the resulting logs.

### kind

```bash
# Run HTTP test (spins up a temporary kind cluster, tests, and tears it down)
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh

# Retain the kind cluster after testing for manual review and debugging
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --keep-cluster
```

### OpenShift

```bash
# Target an OpenShift cluster (skips kind setup, uses the openshift overlay)
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --openshift

# Target a specific OpenShift context (implies --openshift)
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --context <ctx>
```

### Understanding Log Results

- **TCP_HIT:** Served fast from disk cache.
- **TCP_MEM_HIT:** Served ultra-fast from memory cache.
- **TCP_MISS:** Downloaded from the origin server (not in cache).
- **TCP_TUNNEL:** Encrypted HTTPS traffic passed through blindly (not cached).

For more detailed explanations of log statuses and monitoring cache hit rates, see the [Squid monitoring guide](https://oneuptime.com/blog/post/2026-03-20-squid-monitor-cache-hit-rates-ipv4/view).

## 🔒 HTTPS Caching

By default, proxies cannot see inside encrypted HTTPS traffic. Here is how Squid manages encrypted flows depending on your configuration:

* **Blind Tunneling (CONNECT):** Passes encrypted TCP traffic through an opaque tunnel. 
    * *Trade-off:* Zero visibility; no caching or granular filtering is possible.
* **Full Decryption ([SSL Bump](https://wiki.squid-cache.org/Features/SslBump) MITM):** Intercepts, decrypts, and re-encrypts traffic using a custom Root CA. 
    * *Trade-off:* Enables full inspection and caching, but requires complex certificate management and raises privacy/legal risks.
* **Smart Inspection ([Peek & Splice](https://wiki.squid-cache.org/Features/SslPeekAndSplice)):** Inspects the unencrypted SNI (Server Name Indication) during the TLS handshake. 
    * *Trade-off:* Allows domain-based filtering without requiring full decryption.

**Modern Constraints: TLS 1.3 & ECH**:
While Squid supports TLS 1.3, new privacy standards like ECH (Encrypted Client Hello) and ESNI encrypt the destination domain itself. Since the proxy cannot see the target to apply policy, these connections must be spliced (passed through blindly) to prevent connection failure.

> **Warning:**  SSL Bump breaks end-to-end trust. Always ensure you have legal and compliance approval before intercepting HTTPS traffic.

### Automated Tests

#### kind

```bash
# Run SSL Bump test (builds image, generates CA, deploys, and verifies cache)
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --mode ssl-bump

# Keep the cluster for manual inspection after the test
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --mode ssl-bump --keep-cluster
```

#### OpenShift

The SSL Bump image must be pushed to a registry accessible by your cluster. Run these two steps once each.

```bash
export SQUID_IMAGE_REGISTRY="image-registry"
```

**Step 1 — build and push the image (run once per image change):**

```bash
# --build-push implies --mode ssl-bump
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --build-push
```

Make the pushed image accessible from your cluster, then proceed to step 2.

**Step 2 — deploy and test:**

```bash
./deploy/components/multimedia-downloader/implementations/squid/test/test-squid.sh --mode ssl-bump --openshift
```

(Note: You can always pass --registry <url> manually if you prefer not to use the environment variable).

## 🛠️ Manual Deployment

> **Note:** The following steps assume your target cluster is already running and your active `kubectl` context is set correctly.

### Standard HTTP Proxy

**Step 1 — deploy:**

#### kind

```bash
kubectl apply -k deploy/components/multimedia-downloader/implementations/squid/http/overlays/kind
kubectl apply -f deploy/components/multimedia-downloader/service.yaml
kubectl rollout status deployment/multimedia-downloader --timeout=120s
```

#### OpenShift

```bash
kubectl apply -k deploy/components/multimedia-downloader/implementations/squid/http/overlays/openshift
kubectl apply -f deploy/components/multimedia-downloader/service.yaml
kubectl rollout status deployment/multimedia-downloader --timeout=120s
```

**Step 2 — verify:**

Run two requests through the proxy. The first should be a cache miss, the second a cache hit:

```bash
kubectl run curl-test --image=curlimages/curl:8.11.1 --restart=Never -- sleep 30
kubectl wait --for=condition=Ready pod/curl-test --timeout=60s
kubectl exec curl-test -- curl -sf -x http://multimedia-downloader:80 -o /dev/null -w "miss: %{http_code}\n" http://images.cocodataset.org/val2017/000000039769.jpg
kubectl exec curl-test -- curl -sf -x http://multimedia-downloader:80 -o /dev/null -w "hit:  %{http_code}\n" http://images.cocodataset.org/val2017/000000039769.jpg
```

### SSL Bump Proxy (Advanced)

Because SSL Bump intercepts encrypted traffic, it requires a custom CA certificate, a specialized container image, and platform-specific overlays.

| Overlay | Path | Image |
|---------|------|-------|
| `kind` | `https-ssl-bump/overlays/kind` | `squid-ssl-bump:local` (locally built) |
| `openshift` | `https-ssl-bump/overlays/openshift` | your registry — set `$SQUID_IMAGE_REGISTRY` or `--registry` |

**Step 1 — generate the CA certificate** (must exist before deploying — the deployment mounts both secrets):

| Secret | Contents | Mounted by |
|--------|----------|------------|
| `squid-ssl-certs` | CA cert + private key | Squid proxy (signs intercepted TLS) |
| `squid-ca-public-cert` | CA cert only | Client pods (trust the proxy CA) |


To automatically generate the secrets run:
```bash
./deploy/components/multimedia-downloader/implementations/squid/test/generate-ssl-certs.sh
```

**Step 2 — deploy:**

Build the [Dockerfile](docker/Dockerfile.squid-ssl-bump) (required for both platforms):

```bash
docker build -t squid-ssl-bump:local \
  -f deploy/components/multimedia-downloader/implementations/squid/docker/Dockerfile.squid-ssl-bump \
  deploy/components/multimedia-downloader/implementations/squid/docker/
```

#### kind

```bash
kind load docker-image squid-ssl-bump:local
kubectl apply -k deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/overlays/kind
kubectl apply -f deploy/components/multimedia-downloader/service.yaml
kubectl rollout status deployment/multimedia-downloader --timeout=120s
```

#### OpenShift

```bash
export SQUID_IMAGE_REGISTRY=<your-registry>
docker tag squid-ssl-bump:local ${SQUID_IMAGE_REGISTRY}/squid-ssl-bump:dev
docker push ${SQUID_IMAGE_REGISTRY}/squid-ssl-bump:dev
kubectl apply -k deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/overlays/openshift
kubectl set image deployment/multimedia-downloader squid=${SQUID_IMAGE_REGISTRY}/squid-ssl-bump:dev
kubectl apply -f deploy/components/multimedia-downloader/service.yaml
kubectl rollout status deployment/multimedia-downloader --timeout=120s
```

**Step 3 — verify:**

Deploy the CA-trusting test pod and run two requests. The first should be a cache miss, the second a cache hit:

```bash
kubectl apply -f deploy/components/multimedia-downloader/implementations/squid/test/curl-test-pod.yaml
kubectl wait --for=condition=Ready pod/curl-ssl-test-pod --timeout=120s
kubectl exec curl-ssl-test-pod -- curl -sf -o /dev/null -w "miss: %{http_code}\n" https://images.dog.ceo/breeds/poodle-standard/n02113799_2280.jpg
kubectl exec curl-ssl-test-pod -- curl -sf -o /dev/null -w "hit:  %{http_code}\n" https://images.dog.ceo/breeds/poodle-standard/n02113799_2280.jpg
```

#### Configuring Client Pods (Production)

To route a real workload's traffic through the SSL Bump proxy, you must inject the Squid CA and proxy environment variables. Patch your client's Kustomize deployment:

```yaml
patches:
  - path: path/to/https-ssl-bump/patch-client-deployment.yaml
    target:
      kind: Deployment
      name: <your-client-deployment-name>
```
