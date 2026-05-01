# session-affinity-scorer

Routes subsequent requests in a session to the same pod as the first
request, maximizing KV-cache reuse for multi-turn conversations.

## Type

`session-affinity-scorer`

## How it works

The scorer reads the `x-session-token` header from incoming requests.
On the first request in a session, a pod is selected normally. On
subsequent requests with the same session token, that pod receives the
highest score and all other pods receive zero, pinning the session.

The scorer also implements `ResponseBodyProcessor` to extract and persist
session state from responses.

## Configuration

No parameters. The plugin is stateless per-instance.

```yaml
## When to use

Use for multi-turn chat applications or agentic workflows where the same
context prefix is reused across turns. Avoids KV-cache misses caused by
routing the same session to different pods.
