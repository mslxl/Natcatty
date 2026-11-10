/*
 * MCP tools sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It declares five
 * external MCP tools in plugin.toml and answers the host's mcp_tool_call
 * dispatch command through the mcp_tool_result host import:
 *
 *   echo          read   target none     -> text block + structuredContent
 *   write_note    write  target none     -> text block
 *   echo_session  read   target session  -> text block
 *   fail          read   target none     -> typed guest tool error
 *   slow          read   target none     -> progress, then blocks until cancelled
 *
 * The host owns naming, authorization, timeouts, bounds, audit, and the MCP
 * protocol. The guest only maps one tool call to one result payload. Progress
 * is reported for `echo` and `slow`; the host forwards it only when the MCP
 * client sent a progress token.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;
typedef unsigned long size_t;
typedef unsigned int uintptr_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

/* Freestanding helpers: -nostdlib leaves clang free to lower loops and
 * initializers to these libc names, so the module must define them itself. */
void *memset(void *dst, int value, size_t count) {
  uint8_t *bytes = (uint8_t *)dst;
  size_t i;
  for (i = 0; i < count; i++) {
    bytes[i] = (uint8_t)value;
  }
  return dst;
}

void *memcpy(void *dst, const void *src, size_t count) {
  uint8_t *out = (uint8_t *)dst;
  const uint8_t *in = (const uint8_t *)src;
  size_t i;
  for (i = 0; i < count; i++) {
    out[i] = in[i];
  }
  return dst;
}

size_t strlen(const char *text) {
  size_t length = 0;
  while (text[length] != '\0') {
    length++;
  }
  return length;
}

IMPORT("natcatty_host_v1", "mcp_tool_result")
extern int32_t host_mcp_tool_result(uint32_t request_id, const uint8_t *ptr, int32_t len);
IMPORT("natcatty_host_v1", "mcp_tool_progress")
extern int32_t host_mcp_tool_progress(uint32_t request_id, uint32_t progress,
                                      const uint8_t *ptr, int32_t len);

/* A bump allocator over one static arena. The host copies each dispatch
 * command into this memory; the arena resets on init. */
#define ARENA_BYTES (64 * 1024)
static uint8_t arena[ARENA_BYTES];
static size_t arena_used;

EXPORT("natcatty_alloc")
uint32_t natcatty_alloc(uint32_t size) {
  size_t aligned = ((size_t)size + 7u) & ~(size_t)7u;
  if (aligned > ARENA_BYTES - arena_used) {
    return 0;
  }
  uint32_t offset = (uint32_t)arena_used;
  arena_used += aligned;
  return (uint32_t)(uintptr_t)(arena + offset);
}

EXPORT("natcatty_scratch")
uint32_t natcatty_scratch(void) { return (uint32_t)(uintptr_t)arena; }

EXPORT("natcatty_abi_version")
int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init")
int32_t natcatty_init(void) {
  arena_used = 0;
  return 0;
}

EXPORT("natcatty_poll")
int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown")
int32_t natcatty_shutdown(void) { return 0; }

/* ----- tiny JSON field scanners (host-produced, simple shape) ----- */

static int32_t bytes_equal(const char *left, const char *right, size_t count) {
  size_t i;
  for (i = 0; i < count; i++) {
    if (left[i] != right[i]) {
      return 0;
    }
  }
  return 1;
}

static int32_t tool_is(const char *tool, const char *name) {
  size_t length = strlen(name);
  return strlen(tool) == length && bytes_equal(tool, name, length);
}

/* find_key returns the offset just past `"key":"` or `"key":`, or -1. */
static int32_t find_key(const char *json, int32_t length, const char *key,
                        int32_t quote) {
  size_t key_len = strlen(key);
  int32_t i;
  for (i = 0; i + (int32_t)key_len + 3 < length; i++) {
    if (json[i] != '"') {
      continue;
    }
    if (!bytes_equal(json + i + 1, key, key_len)) {
      continue;
    }
    int32_t after = i + 1 + (int32_t)key_len;
    if (after >= length || json[after] != '"') {
      continue;
    }
    after++;
    if (after >= length || json[after] != ':') {
      continue;
    }
    after++;
    if (after < length && json[after] == '"' && quote) {
      return after + 1;
    }
    if (!quote) {
      return after;
    }
  }
  return -1;
}

/* read_string copies the string starting at offset into out, bounded. */
static int32_t read_string(const char *json, int32_t length, int32_t offset,
                           char *out, int32_t capacity) {
  int32_t i = 0;
  if (offset < 0) {
    out[0] = '\0';
    return 0;
  }
  while (offset + i < length && i < capacity - 1) {
    char character = json[offset + i];
    if (character == '"' || character == '\\') {
      break;
    }
    out[i] = character;
    i++;
  }
  out[i] = '\0';
  return i;
}

static uint32_t read_uint(const char *json, int32_t length, int32_t offset) {
  uint32_t value = 0;
  if (offset < 0) {
    return 0;
  }
  while (offset < length && json[offset] >= '0' && json[offset] <= '9') {
    value = value * 10u + (uint32_t)(json[offset] - '0');
    offset++;
  }
  return value;
}

/* ----- result construction ----- */

static char result[4096];
static int32_t result_len;

static void reset_result(void) { result_len = 0; }

static void append_literal(const char *text) {
  int32_t length = (int32_t)strlen(text);
  int32_t i;
  for (i = 0; i < length && result_len < (int32_t)sizeof(result); i++) {
    result[result_len++] = text[i];
  }
}

static void append_uint(uint32_t value) {
  char digits[11];
  int32_t count = 0;
  if (value == 0) {
    append_literal("0");
    return;
  }
  while (value > 0 && count < 10) {
    digits[count++] = (char)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0) {
    char one = digits[--count];
    if (result_len < (int32_t)sizeof(result)) {
      result[result_len++] = one;
    }
  }
}

static void append_json_string(const char *text) {
  int32_t i;
  for (i = 0; text[i] != '\0' && result_len < (int32_t)sizeof(result) - 2; i++) {
    char character = text[i];
    if (character == '"' || character == '\\') {
      result[result_len++] = '\\';
    }
    result[result_len++] = character;
  }
}

static int32_t deliver(uint32_t request_id) {
  return host_mcp_tool_result(request_id, (const uint8_t *)result, result_len);
}

static int32_t tool_error(uint32_t request_id, const char *code, const char *message) {
  /* Tool errors keep MCP content and structured content: the host marks the
   * result isError and preserves both rather than replacing them with a text
   * error. */
  reset_result();
  append_literal("{\"isError\":true,\"code\":\"");
  append_json_string(code);
  append_literal("\",\"message\":\"");
  append_json_string(message);
  append_literal("\",\"content\":[{\"type\":\"text\",\"text\":\"");
  append_json_string(message);
  append_literal("\"}],\"structuredContent\":{\"failed\":true}}");
  return deliver(request_id);
}

EXPORT("natcatty_dispatch")
int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  const char *command = (const char *)(uintptr_t)command_ptr;
  int32_t length = (int32_t)command_len;
  if (command == 0 || length <= 0) {
    return -1;
  }
  uint32_t request_id = read_uint(command, length, find_key(command, length, "requestId", 0));
  char tool[128];
  read_string(command, length, find_key(command, length, "tool", 1), tool, (int32_t)sizeof(tool));
  char text[512];
  read_string(command, length, find_key(command, length, "text", 1), text, (int32_t)sizeof(text));
  if (request_id == 0) {
    return -1;
  }

  if (tool_is(tool, "plugin.acme.mcp-tools.echo")) {
    host_mcp_tool_progress(request_id, 50, (const uint8_t *)"halfway", 7);
    reset_result();
    append_literal("{\"content\":[{\"type\":\"text\",\"text\":\"echo: ");
    append_json_string(text);
    append_literal("\"}],\"structuredContent\":{\"tool\":\"echo\",\"text\":\"");
    append_json_string(text);
    append_literal("\",\"requestId\":");
    append_uint(request_id);
    append_literal("}}");
    return deliver(request_id);
  }
  if (tool_is(tool, "plugin.acme.mcp-tools.write_note")) {
    reset_result();
    append_literal("{\"content\":[{\"type\":\"text\",\"text\":\"wrote\"}],"
                   "\"structuredContent\":{\"tool\":\"write_note\",\"wrote\":true}}");
    return deliver(request_id);
  }
  if (tool_is(tool, "plugin.acme.mcp-tools.echo_session")) {
    reset_result();
    append_literal("{\"content\":[{\"type\":\"text\",\"text\":\"session ok\"}],"
                   "\"structuredContent\":{\"tool\":\"echo_session\"}}");
    return deliver(request_id);
  }
  if (tool_is(tool, "plugin.acme.mcp-tools.fail")) {
    return tool_error(request_id, "guest_failed", "guest tool failed");
  }
  if (tool_is(tool, "plugin.acme.mcp-tools.slow")) {
    /* Report that the call is running, then block. The host stops the module
     * when the MCP client disconnects, the declared deadline expires, or the
     * plugin is unloaded, so no result is ever delivered here. */
    host_mcp_tool_progress(request_id, 1, (const uint8_t *)"started", 7);
    for (;;) {
    }
  }
  return tool_error(request_id, "unknown_tool", "guest does not know the tool");
}
