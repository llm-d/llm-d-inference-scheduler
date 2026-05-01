# load-aware-scorer

Scores pods based on their combined queue depth and active request load,
normalized against a configurable threshold.

## Type

`load-aware-scorer`

## How it works

Computes a load score for each pod using its waiting queue size and
active request count. Pods with load below the threshold receive higher
scores. Once load exceeds the threshold, the score approaches zero.

## Configuration

```yaml
## When to use

Use as a general-purpose load balancer when you want to spread requests
evenly across pods. Can be combined with prefix cache scorers to balance
load against cache affinity.

> **Note:** This scorer is deprecated in favor of `queue-depth-scorer`
> and `active-request-scorer` which provide more granular control.
