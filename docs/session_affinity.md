# Session Affinity in LLM-D Inference Scheduler

## Overview

Session Affinity ensures that subsequent requests from the same client are routed to the same inference pod. This is implemented using standard HTTP cookies, which are automatically handled by browsers and HTTP clients without requiring any client-side code changes.

## Benefits

- **Improved KV Cache Hit Rates**: Multi-turn conversations benefit from cached prompt prefixes on the same pod
- **Reduced Latency**: Eliminates the need to rebuild context on different pods
- **Better Resource Utilization**: Concentrates session state on specific pods
- **Transparent to Clients**: Uses standard HTTP cookies that browsers handle automatically

## Quick Start

The session affinity plugin is already compiled into the EPP binary. To enable it:

1. **Add to your EPP configuration:**
   ```yaml
   plugins:
     - type: session-affinity-scorer
   schedulingProfiles:
     - name: default
       plugins:
         - pluginRef: session-affinity-scorer
           weight: 100
   ```

2. **Deploy with the configuration:**
   ```bash
   # For KIND development (using environment variable)
   export SESSION_AFFINITY_ENABLED=true
   make env-dev-kind
   
   # Or specify config file directly
   export EPP_CONFIG=deploy/config/session-affinity-config.yaml
   make env-dev-kind
   
   # For Kubernetes
   export SESSION_AFFINITY_ENABLED=true
   ./scripts/kubernetes-dev-env.sh
   
   # Or with custom config
   export EPP_CONFIG=deploy/config/session-affinity-config.yaml
   ./scripts/kubernetes-dev-env.sh
   ```

3. **Test it:**
   ```bash
   curl -s -w '\n' -v http://localhost:30080/v1/completions \
     -H 'Content-Type: application/json' \
     -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' | jq
   # Look for Set-Cookie header in response
   ```

## How It Works

### First Request

When a client makes their first request:

1. The request arrives without a session cookie
2. The scheduler routes it based on other scoring criteria (e.g., prefix cache, load)
3. The selected pod processes the request
4. The scheduler adds a `Set-Cookie` header to the response with the pod identifier

### Subsequent Requests

For follow-up requests in the same session:

1. The client automatically includes the `llm-d-session` cookie
2. The session affinity scorer gives the previously-used pod a score of 1.0
3. All other pods receive a score of 0.0
4. The request is routed to the same pod (assuming it's still available)

## Cookie Details

- **Name**: `llm-d-session`
- **Value**: Base64-encoded pod identifier (format: `namespace/pod-name`)
- **Path**: `/` (applies to all paths)
- **HttpOnly**: `true` (prevents JavaScript access for security)
- **SameSite**: `Lax` (provides CSRF protection)
- **Secure**: Not set by default (should be enabled in production with HTTPS)
- **Max-Age**: Not set (session cookie, expires when browser closes)

## Configuration

### Basic Configuration

Add the `session-affinity-scorer` plugin to your EPP configuration:
## Deployment

### Step 1: Choose Configuration File

You have several pre-built configuration files:

- **`deploy/config/session-affinity-config.yaml`** - Basic configuration with comments
- **`deploy/config/session-affinity-production-config.yaml`** - Production-ready with secure settings
- **`deploy/config/epp-config.yaml`** - Minimal config (add session affinity to this)

Or create your own based on the examples below.

### Step 2: Create ConfigMap

**Option A: Using kubectl**
```bash
kubectl create configmap epp-config \
  --from-file=config.yaml=deploy/config/session-affinity-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
```

**Option B: Using kustomize**

Add to your `kustomization.yaml`:
```yaml
configMapGenerator:
- name: epp-config
  files:
  - config.yaml=../../config/session-affinity-config.yaml
```

### Step 3: Deploy

**For KIND Development Environment:**
```bash
# Set the config file
export EPP_CONFIG=deploy/config/session-affinity-config.yaml

# Deploy the stack
make env-dev-kind

# Access via NodePort (default: 30080)
# Or port forward: kubectl port-forward service/inference-gateway 8080:80
```

**For Production Kubernetes:**
```bash
# Apply your kustomization
kubectl apply -k deploy/environments/dev/kind-istio/

# Or apply components directly
kubectl apply -f deploy/components/inference-gateway/
```

### Step 4: Verify Deployment

Check that the EPP loaded the plugin:
```bash
# View EPP logs
kubectl logs -l app=epp -f

# Look for:
# "Registered plugin" plugin="session-affinity-scorer"
```

Test session affinity:
```bash
# Make first request
curl -s -w '\n' -v http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' 2>&1 | grep -i set-cookie

# Check response headers for:
# Set-Cookie: llm-d-session=...; Path=/; HttpOnly; SameSite=Lax

# Make second request with cookie (browser includes cookie automatically)
curl -s -w '\n' http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -H 'Cookie: llm-d-session=<value-from-first-response>' \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"how are you","max_tokens":10,"temperature":0}' | jq
```


```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: session-affinity-scorer
- type: prefix-cache-scorer
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: max-score-picker
  - pluginRef: session-affinity-scorer
    weight: 100  # High weight ensures session affinity takes precedence
  - pluginRef: prefix-cache-scorer
    weight: 50
```

### Weight Recommendations

- **High Weight (100+)**: Session affinity takes precedence over other factors
- **Medium Weight (50-100)**: Balanced with other scorers like prefix cache
- **Low Weight (1-50)**: Session affinity is a tiebreaker

### Combined with Other Scorers

Session affinity works well with other scorers:

```yaml
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: decode-filter
  - pluginRef: max-score-picker
  - pluginRef: session-affinity-scorer
    weight: 100  # Highest priority for existing sessions
  - pluginRef: precise-prefix-cache-scorer
    weight: 50   # Cache locality for new sessions
  - pluginRef: load-aware-scorer
    weight: 25   # Load balancing
```

## Client Usage

### Automatic (Browsers)

Browsers automatically handle cookies. No code changes needed:

```javascript
// First request - no cookie
fetch('https://gateway/v1/chat/completions', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    model: 'llama',
    messages: [{ role: 'user', content: 'Hello' }]
  })
});

// Subsequent requests - browser automatically includes cookie
fetch('https://gateway/v1/chat/completions', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    model: 'llama',
    messages: [
      { role: 'user', content: 'Hello' },
      { role: 'assistant', content: 'Hi there!' },
      { role: 'user', content: 'How are you?' }
    ]
  })
});
```

### Manual (CLI/Scripts)

For CLI tools or scripts, you need to preserve cookies between requests:

```bash
# Using curl with cookie jar
curl -s -w '\n' http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -c cookies.txt \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' | jq

# Subsequent request with cookie
curl -s -w '\n' http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -b cookies.txt \
  -c cookies.txt \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"how are you","max_tokens":10,"temperature":0}' | jq
```

### Python Example

```python
import requests

# Create a session to automatically handle cookies
session = requests.Session()

# First request
response = session.post(
    'http://localhost:30080/v1/completions',
    json={
        'model': 'TinyLlama/TinyLlama-1.1B-Chat-v1.0',
        'prompt': 'hi',
        'max_tokens': 10,
        'temperature': 0
    }
)
print(response.json())

# Subsequent requests automatically include the session cookie
response = session.post(
    'http://localhost:30080/v1/completions',
    json={
        'model': 'TinyLlama/TinyLlama-1.1B-Chat-v1.0',
        'prompt': 'how are you',
        'max_tokens': 10,
        'temperature': 0
    }
)
print(response.json())
```

## Behavior

### Pod Availability

If the pod specified in the session cookie is not available (e.g., scaled down, crashed):
- The session affinity scorer gives it a score of 0.0 (same as other unavailable pods)
- The request is routed based on other scoring criteria
- A new session cookie is set with the newly selected pod

### Load Balancing

Session affinity can be balanced with load-aware scoring:

```yaml
plugins:
  - pluginRef: session-affinity-scorer
    weight: 80   # Strong preference for session pod
  - pluginRef: load-aware-scorer
    weight: 20   # But consider load
```

This allows the scheduler to route to a different pod if the session pod is overloaded.

## Security Considerations

### Production Deployment

For production deployments with HTTPS, consider:

1. **Enable Secure Flag**: Modify the code to set `Secure: true` in the cookie
2. **Set Max-Age**: Add expiration time to limit cookie lifetime
3. **Use SameSite=Strict**: For stricter CSRF protection if appropriate

### Privacy

The session cookie contains:
- Pod namespace and name (base64-encoded)
- No user data or sensitive information
- No personally identifiable information (PII)

## Monitoring

### Metrics

Monitor session affinity effectiveness:
- Cache hit rates per pod
- Request distribution across pods
- Session duration and request counts

### Debugging

To inspect the session cookie:

```bash
# Decode the cookie value
echo "ZGVmYXVsdC9wb2QtMQ==" | base64 -d
# Output: default/pod-1
```

## Troubleshooting

### Cookie Not Being Set

Check:
1. Response headers include `Set-Cookie`
2. EPP configuration includes `session-affinity-scorer`
3. Plugin is in the scheduling profile with appropriate weight

### Cookie Not Being Sent

Check:
1. Client is configured to send cookies
2. Cookie path matches request path
3. SameSite policy allows the request

### Requests Not Sticky

Check:
1. Session affinity scorer weight is high enough
2. Target pod is available and healthy
3. Cookie value is valid and not corrupted

## Migration from Custom Headers

If you were using the old `x-session-token` header approach:

1. Update to the latest version with cookie support
2. No client changes needed - cookies are handled automatically
3. Old header-based sessions will naturally expire
4. New sessions will use cookies

## Related Features

- **Prefix Cache Scorer**: Works well with session affinity for cache locality
- **Load Aware Scorer**: Can be balanced with session affinity
- **P/D Disaggregation**: Session affinity applies to decode pods

## References

- [Architecture Documentation](./architecture.md)
- [Plugin Configuration](./architecture.md#plugin-configuration)
- [HTTP Cookie Specification (RFC 6265)](https://tools.ietf.org/html/rfc6265)