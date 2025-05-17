#include <stdbool.h>
#include <stdint.h>

struct EncodeOptions {
  bool add_special_token;
  bool return_type_ids;
  bool return_tokens;
  bool return_special_tokens_mask;
  bool return_attention_mask;
  bool return_offsets;
};

struct TokenizerOptions {
  bool encode_special_tokens;
};

struct Buffer {
  uint32_t *ids;
  uint32_t *type_ids;
  uint32_t *special_tokens_mask;
  uint32_t *attention_mask;
  char *tokens;
  size_t *offsets;
  uint32_t len;
};

const char *version();

void *from_bytes(const uint8_t *config, uint32_t len, const struct TokenizerOptions *options);

void *from_bytes_with_truncation(const uint8_t *config, uint32_t len, uint32_t max_len, uint8_t direction);

void *from_file(const char *config);

struct Buffer encode(void *ptr, const char *message, const struct EncodeOptions *options);

char *decode(void *ptr, const uint32_t *ids, uint32_t len, bool skip_special_tokens);

uint32_t vocab_size(void *ptr);

void free_tokenizer(void *ptr);

void free_buffer(struct Buffer buffer);

void free_string(char *string);
