# active-request-scorer

Scores pods based on the number of actively in-flight requests being served.
Each request is tracked individually with a configurable TTL to handle stale
entries from timed-out requests.

## Type

`active-request-scorer`

## How it works

Pods with active request count at or below `idleThreshold` receive a score
of `1.0`. Busier pods receive lower scores proportional to their load,
down to `0.0` for the most loaded pod. A `maxBusyScore` parameter creates
a scoring gap between idle and busy pods to strongly prefer idle pods.

## Configuration

```yaml
## When to use

Use when you want to route away from pods that are actively serving many
requests. Pairs well with `queue-depth-scorer` for comprehensive load
awareness.
