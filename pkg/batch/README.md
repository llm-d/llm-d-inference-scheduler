# Batch Processor

## Overview
The batch processor (BP) provides asynchronous workflows for variable SLO-based inference requests.


## Architecture

An underlying implementation should provide persistent messaging that adhere to the interface defined in [api.go](api.go).

A pluggable request policy is used to merge multiple request channels into a single request channel on which the batch worker is listening.

An example for such a policy is a [Random Robin Policy](random_robin_policy.go).

Each [Batch Processor worker](worker.go) is responsible for pulling requests from the merged request channel, submit to the IGW and apply retry logic if needed. 



### Requests

Request messages should have the following format:
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

### Results

Messages on the results channel will have the following structure:

```json
{
    "id" : "id mapped to the request",
    "payload" : {/*inference payload*/} ,
    // or
    "error" : "error's reason"
}
```


## Implementations

### Redis

An example implementation based on Redis is provided which behaves as follows:

- Redis Lists as the request queues.
- Redis Sorted Set as the retry exponential backoff implementation.
- Redis List as the result queue.
