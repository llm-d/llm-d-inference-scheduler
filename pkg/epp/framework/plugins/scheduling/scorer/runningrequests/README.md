# Running Requests Size Scorer Plugin

**Type:** `running-requests-size-scorer`

This plugin scores candidate endpoints based on how many requests are currently running on each model server.

## What it does

For each scheduling cycle, the plugin reads `RunningRequestsSize` from endpoint metrics and computes a normalized score:

$$
\text{score(endpoint)} = \frac{\text{maxRunning} - \text{running(endpoint)}}{\text{maxRunning} - \text{minRunning}}
$$

So:

- fewest running requests → score `1.0`
- most running requests → score `0.0`
- others are linearly scaled between them

If all endpoints have the same running request count, every endpoint receives a neutral score of `1.0`.

## Scheduling intent

The scorer returns category `Distribution`, helping spread requests away from endpoints that are already busy processing the most in-flight requests.

## Inputs consumed

The plugin consumes:

- `metrics.RunningRequestsSizeKey` (`int`)

## Configuration

This scorer currently has no runtime parameters.

**Configuration Example:**
```yaml
plugins:
  - type: running-requests-size-scorer
    name: running-requests
schedulingProfiles:
  - name: default
    plugins:
      - pluginRef: running-requests
        weight: 1
```
