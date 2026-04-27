# vLLM gRPC Parser Plugin

**Type:** `vllmgrpc-parser`

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
- [Parsers Index](../README.md)
