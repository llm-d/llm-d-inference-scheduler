# Session Affinity in LLM-D Inference Scheduler

## Overview

Session Affinity ensures that subsequent requests from the same client are routed to the same inference pod. This is implemented using standard HTTP cookies, which are automatically handled by browsers and HTTP clients without requiring any client-side code changes.

## Benefits

- **Improved KV Cache Hit Rates**: Multi-turn conversations benefit from cached prompt prefixes on the same pod
- **Reduced Latency**: Eliminates the need to rebuild context on different pods
- **Better Resource Utilization**: Concentrates session state on specific pods
- **Transparent to Clients**: Uses standard HTTP cookies that browsers handle automatically

### Pod Availability

If the pod specified in the session cookie is not available (e.g., scaled down, crashed):
- The session affinity scorer gives it a score of 0.0 (same as other unavailable pods)
- The request is routed based on other scoring criteria
- A new session cookie is set with the newly selected pod


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
           weight: 70  # Adjust weight based on your requirements
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

## Configuration

### Basic Configuration (Session Cookie)

The simplest configuration uses a session cookie (expires when browser closes):

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: session-affinity-scorer
  # No parameters needed for session cookie (default behavior)
- type: prefix-cache-scorer
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: max-score-picker
  - pluginRef: session-affinity-scorer
    weight: 70  # Adjust based on how strongly you want session affinity
  - pluginRef: prefix-cache-scorer
    weight: 50
```

### Configuration with Persistent Cookie (Optional)

To set a cookie expiration time, use the `maxAge` parameter (in seconds):

```yaml
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: session-affinity-scorer
  parameters:
    maxAge: 3600  # Cookie expires after 1 hour
    # Other options:
    # maxAge: 86400   # 24 hours
- type: prefix-cache-scorer
- type: max-score-picker
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: max-score-picker
  - pluginRef: session-affinity-scorer
    weight: 70
  - pluginRef: prefix-cache-scorer
    weight: 50
```


## Verify Deployment

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
Note: modern browsers check CORS (Cross-Origin Resource Sharing) settings and can prevent cookies sending even if "Set-Cookie" exist. Please check your browser configuration.



### Manual (CLI/Scripts)

For CLI tools or scripts, you need to preserve cookies between requests:

```bash
# Using curl with cookie jar
curl -v '\n' http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -c cookies.txt \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"hi","max_tokens":10,"temperature":0}' | jq

# Subsequent request with cookie
curl -v '\n' http://localhost:30080/v1/completions \
  -H 'Content-Type: application/json' \
  -b cookies.txt \
  -c cookies.txt \
  -d '{"model":"TinyLlama/TinyLlama-1.1B-Chat-v1.0","prompt":"how are you","max_tokens":10,"temperature":0}' | jq
```
Check the request and response headers to see the cookie being sent.

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

