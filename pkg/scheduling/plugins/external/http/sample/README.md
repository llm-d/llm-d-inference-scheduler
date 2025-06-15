# Sample external plugins HTTP server

A Python HTTP web server that provides plugins endpoints for llm-d-inference-scheduler.

## Overview

This server implements two main endpoints:
- **Filter Endpoint**: Filters available pods based on scheduling context and constraints

## Features

- HTTP server listening on configurable port (default: 1080)
- JSON request/response handling
- Comprehensive request validation
- Logging with structured output
- Error handling and HTTP status codes

## File Structure

```
.
├── server.py          # Main server implementation
├── README.md          # This documentation
├── requirements.txt   # Python dependencies (if any)
```

## API Endpoints

### POST /external/sample/filter

Filters available pods based on sample logic (pod name ends with '2').

**Request Structure:**
```json
{
  "sched_context": {
    "request": {
      "target_model": "string",
      "request_id": "string", 
      "critical": boolean,
      "prompt": "string",
      "headers": {}
    },
    "pods": [
      {
        "pod": {
          "namespaced_name": {
            "name": "string",
            "namespace": "string"
          },
          "address": "string",
          "labels": {}
        },
        "metrics": {
          "active_models": {},
          "waiting_models": {},
          "max_active_models": integer,
          "running_queue_size": integer,
          "waiting_queue_size": integer,
          "kv_cache_usage_percent": float,
          "kv_cache_max_token_capacity": integer
        },
        "update_time": "string"
      }
    ]
  },
  "pods": [/* same structure as sched_context.pods */]
}
```

**Response:**
```json
[
  {
    "name": "string",
    "namespace": "string"
  }
]
```

## Installation & Setup

### Prerequisites

- Python 3.7 or higher
- No external dependencies required (uses only Python standard library)

### Quick Start

1. **Run the server**
   ```bash
   python server.py
   # or
   python3 server.py
   ```

2. **Run with custom port/host**
   ```bash
   python server.py 8080                    # Custom port
   python server.py 8080 0.0.0.0           # Custom port and host
   ```

### Sample Request Body
```json
{
  "sched_context": {
    "request": {
      "target_model": "llama-7b",
      "request_id": "req-12345",
      "critical": false,
      "prompt": "Hello, world!",
      "headers": {
        "user-agent": "test-client"
      }
    },
    "pods": []
  },
  "pods": [
    {
      "pod": {
        "namespaced_name": {
          "name": "model-server-1",
          "namespace": "ml-serving"
        },
        "address": "10.0.1.100",
        "labels": {
          "model": "llama-7b",
          "gpu": "true"
        }
      },
      "metrics": {
        "active_models": {"llama-7b": 2},
        "waiting_models": {},
        "max_active_models": 4,
        "running_queue_size": 2,
        "waiting_queue_size": 1,
        "kv_cache_usage_percent": 65.5,
        "kv_cache_max_token_capacity": 8192
      },
      "update_time": "2025-06-09T10:30:00Z"
    }
  ]
}
```

## Configuration

### Command Line Arguments
```bash
python server.py [PORT] [HOST]

# Examples:
python server.py 8080                    # Port 8080, localhost
python server.py 8080 0.0.0.0           # Port 8080, all interfaces
```

### Docker
```dockerfile
FROM python:3.9-slim
WORKDIR /app
COPY server.py .
EXPOSE 1080
CMD ["python", "server.py", "1080", "0.0.0.0"]
```

### Endpoint Picker configuration

To add this filter plugin to the EPP, the following environment variable should be defined:

```yaml
env:
- name: EXTERNAL_HTTP_PREFILL_FILTERS
  value: '[{"name":"load", "url":"http://sample-plugins-server-service:80/external/sample"}]'
```

### Install on cluster
```bash
make image-build
kind load docker-image ghcr.io/llm-d/sample-external-plugins:dev --name <cluster-name>
k apply -f ./deployment.yaml
k port-forward svc/sample-plugins-server-service 8800:80
```
