# Squid Proxy: Multimedia Cache

This Squid proxy implementation caches multimedia content (like images and videos) to optimize data retrieval speeds.

> **Note:** Tested on `kind`. If using a different cluster, verify compatibility with your environment's specific security permissions, storage, and resource requirements.

## 🚀 Automated Testing

Use the provided [script](http/test-squid-kind.sh) to spin up a temporary Kind cluster, run cache tests, and verify logs:

```bash
# Run automated test (creates temporary cluster, tests, and cleans up)
./deploy/components/multimedia-downloader/implementations/squid/http/test-squid-kind.sh

# Keep the cluster for debugging and manual testing
./deploy/components/multimedia-downloader/implementations/squid/http/test-squid-kind.sh --keep-cluster
```

### Understanding Log Results

- TCP_HIT: Served fast from disk cache.

- TCP_MEM_HIT: Served ultra-fast from memory cache.

- TCP_MISS: Downloaded from the origin server (not in cache).

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


## Test SSL Bump

The SSL Bump variant requires a custom Docker image:

```bash
docker build -t squid-ssl-bump:local \
  -f deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/Dockerfile.squid-ssl-bump \
  deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/
```

This [test script](https-ssl-bump/test-squid-ssl-bump-kind.sh) builds the image, generates a CA, deploys the proxy and client, and tests the cache end-to-end:

```bash
# Run automated test (creates temporary cluster, tests, and cleans up)
./deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/test-squid-ssl-bump-kind.sh

# Keep the cluster for manual inspection and debugging
./deploy/components/multimedia-downloader/implementations/squid/https-ssl-bump/test-squid-ssl-bump-kind.sh --keep-cluster
```
### Configuring Client Pods for Production

To route a real workload's traffic through the SSL Bump proxy, inject the Squid CA and environment variables by patching your Kustomize deployment:

```yaml
patches:
  - path: path/to/https-ssl-bump/patch-client-deployment.yaml
    target:
      kind: Deployment
      name: <your-client-deployment-name>
```
