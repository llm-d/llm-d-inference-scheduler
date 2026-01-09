# Batch Processor - User Guide

## Overview
There are certain usecases where the inference requests are not latency sensitive (i.e., cardinality of the required SLO >= minutes). For these usecases, an asynchronous, queue-based support is required.


The batch processor is a composable component that provides services in managing such requests.

The main design principles of the batch processor:
* <u>Bring your own queue (BYOQ)</u> - decouple all aspects of prioritization, routing, retries and scaling from the message queue implementation.
* <u>Composability</u> - the end-user doesn't interact directly with the batch processor (there is no API for it). Instead the batch processor only interacts with the message queues.

## Motivation - Use Case (Better Utilization)

In many environments, expensive AI accelerators sit idle during slight dips in real-time demand. 

<u>The Scenario:</u> Accelerators are at 80% capacity with real-time inference traffic. We have latency-tolerant batch that can be send inbetween to make use of the idle capacity of the accelerators.

<u>Value Proposition in this scenario</u>:
* Maximize ROI: Achieve better utilization of your provisioned hardware without impacting the latency of critical traffic.
* Silent Background Processing: Batch jobs are processed "in the cracks" of real-time traffic, effectively making the compute for these jobs "free" since the hardware was already provisioned.
* Resilience by Design: If real-time traffic spikes, the system triggers intelligent retries for the batch jobs, ensuring they eventually complete without manual intervention.


## Table of Contents

- [Overview](#overview)
- [Motivation](#motivation)
- [Installation](#installation)
- [Command line parameters](#command-line-parameters)
- [Request Messages and Consusmption](#request-messages-and-consomption)
    - [Request Merge Policy](#request-merge-policy)
- [Retries](#retries)
- [Results](#results)   
- [Implementations](#implementations)
    - [Redis Channels](#redis-channels)
      - [Redis Command line parameters](#redis-command-line-parameters)
    - [GCP Pub/Sub](#gcp-pub-sub)
- [Development](#development)


## Installation

Currently there is no Helm chart for the Batch Processor.
You can create the docker image for the batch processor with:

```bash 
make image-build-batch
```

When deploying the Batch Processor, make sure that the message queue is ready as the BP will attempt to subscribe on startup.

## Command line parameters

- `concurrency`: the number of concurrenct batch workers, default is 8.
- `endpoint`: Inference gateway endppoint. Batch requests will be sent to this endpoint.
- `inferenceObjective`: InferenceObjective to use for requests (set as the HTTP header x-gateway-inference-objective if not empty). 
- `request-merge-policy`: Currently only supporting <u>random-robin</u> policy.
- `message-queue-impl`: Currently only supporting <u>redis-pubsub</u> for ephemeral Redis-based implementation.

<i>additional parameters may be specified for concrete message queue implementations</i>


## Request Messages and Consusmption

The batch processor expects request messages to have the following format:
```json
{
    "id" : "unique identifier for result mapping",
    "deadline" : "deadline in Unix seconds",
    "payload" : {regular inference payload}
}
```

Example:
```json
{
    "id" : "19933123533434",
    "deadline" : "1764045130",
    "payload": {"model":"food-review","prompt":"hi", "max_tokens":10,"temperature":0}
}
```

### Request Merge Policy

The Batch Processor supports multiple request message queues. A `Request Merge Policy` can be specified to define the merge strategy of messages from the different queues.

Currently the only policy supported is `Random Robin Policy` which randomly picks messages from the queues.




## Retries

When a message processing has failed, either shedded or due to a server-side error, it will be scheduled for a retry (assuming the deadline has not passed).

The batch processor supports exponential-backoff and fixed-rate backoff (TBD).

## Results

Results will be written to the results queue and will have the following structure:

```json
{
    "id" : "id mapped to the request",
    "payload" : {/*inference payload*/} ,
    // or
    "error" : "error's reason"
}
```

## Implementations

### Redis Channels

An example implementation based on Redis channels is provided.

- Redis Channels as the request queues.
- Redis Sorted Set as the retry exponential backoff implementation.
- Redis Channel as the result queue.


![Batch Processor - Redis architecture](/docs/images/batch_processor_redis_architecture.png "BP - Redis")

#### Redis Command line parameters

- `redis.request-queue-name`: The name of the channel for the requests. Default is <u>batch-queue</u>.
- `redis.retry-queue-name`: The name of the channel for the retries. Default is <u>batch-sortedset-retry</u>.
- `redis.result-queue-name`: The name of the channel for the results. Default is <u>batch-queue-result</u>.


### GCP Pub/Sub

TBD

## Development

You can set the KIND environment with batch processor deployed with:
```bash
export BATCH_REDIS_ENABLED=true
make env-dev-kind
```

This will deploy a Redis server as the message queue.

Then, in a new terminal window register a subscriber:

```bash
kubectl exec `kubectl get pods -l app=redis -o=jsonpath='{.items[0].metadata.name}'` -- /usr/local/bin/redis-cli SUBSCRIBE batch-queue-result
```

Publish a message for batch processing:
```bash
kubectl exec `kubectl get pods -l app=redis -o=jsonpath='{.items[0].metadata.name}'` -- /usr/local/bin/redis-cli PUBLISH batch-queue '{"id" : "testmsg", "payload":{ "model":"food-review", "prompt":"hi"}, "deadline" :"9999999999" }'
```
