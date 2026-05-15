# Session Affinity Scorer

**Type:** `session-affinity-scorer`

Scores candidate pods by giving a higher score to pods that were previously used for the same session. Enables sticky routing for stateful workloads where reusing the same pod reduces latency or preserves context.

**Parameters:** None.
