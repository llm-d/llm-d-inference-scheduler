# Batch Processor - User Guide

The batch processor helps in workflows where you have requests that are latency tolerant. I.e., SLOs in minutes/ hours instead of seconds.

The batch processor pulls requests from a message queue (or several MQs according to a policy), sends to the Inference Gateway (IGW) and retries if necessary (e.g., message was shedded).

![Batch Processor - Redis architecture](/docs/images/batch_processor_redis_architecture.png "BP - Redis")