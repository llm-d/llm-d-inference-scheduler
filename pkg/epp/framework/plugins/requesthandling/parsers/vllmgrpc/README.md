# vLLM gRPC Parser Plugin

## VllmGRPCParser

**Type:** `vllmgrpc-parser` | **Implementation:** [vllmgrpc.go](vllmgrpc.go)

Parses H2C (HTTP/2 cleartext) requests and responses in the vLLM gRPC API format. Use this parser when the EPP fronts a vLLM instance that serves its gRPC inference API.

Extracts model name, prompt content, and token metadata from the gRPC request binary framing. Supports the vLLM generate and embed gRPC paths.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: vllmgrpc-parser
    name: vllmgrpc-parser
parser:
  pluginRef: vllmgrpc-parser
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Parsers Index](../README.md)
