# OpenAI Parser Plugin

**Type:** `openai-parser` | **Implementation:** [openai.go](openai.go)

Parses HTTP/H2C requests and responses in the OpenAI API format. This is the default parser and is auto-injected when no parser is specified in `EndpointPickerConfig`.

Supports all standard OpenAI-compatible endpoints: completions, chat/completions, conversations, responses, and embeddings. Extracts model name, prompt content, token counts, and streaming mode from the request body and response.

**Parameters:** None.

**Configuration Example:**
```yaml
plugins:
  - type: openai-parser
    name: openai-parser
parser:
  pluginRef: openai-parser
```

---

## Related Documentation

- [Architecture Overview](../../../../../../../docs/architecture.md)
- [Parsers Index](../README.md)
