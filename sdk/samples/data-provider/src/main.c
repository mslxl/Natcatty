/*
 * Sample data provider guest.
 *
 * This is a real WASM plugin built against the v1.13 data-provider ABI. It
 * serves one non-terminal tabular provider, plugin.acme.data.warehouse. It is
 * not a Host, a Session, or a connection provider: profiles name the provider,
 * the host stores their provider-defined fields, and the guest receives only
 * the field values and resolved secrets for one connection.
 *
 * Supported operations:
 *   open         establishes a connection; with "test":true it returns server
 *                identity and auth verdict instead of a connection token
 *   ping         reports liveness so the host can reuse a pooled connection
 *   capabilities refines the declared feature set
 *   query        returns typed result envelopes ("typed", "rows", "cancel",
 *                "write")
 *   fetch        continues a typed result; the "cancel" query waits for the
 *                host's cooperative cancellation poll before returning rows
 *   cancel       asks the current query to stop
 *   command      returns a summary
 *   begin/commit/rollback, metadata, plan, close
 *
 * Credential plaintext is used only to prove the host resolved it; it is never
 * echoed in a response. Result values keep 64-bit integers and arbitrary
 * decimals as strings, so no precision passes through a float.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned long long uint64_t;
typedef long long int64_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

/* Freestanding helpers: -nostdlib leaves clang free to lower loops and
 * initializers to these libc names, so the module must define them itself. */
void *memset(void *dst, int value, unsigned long count) {
  uint8_t *bytes = (uint8_t *)dst;
  unsigned long i;
  for (i = 0; i < count; i++) {
    bytes[i] = (uint8_t)value;
  }
  return dst;
}

void *memcpy(void *dst, const void *src, unsigned long count) {
  uint8_t *out = (uint8_t *)dst;
  const uint8_t *in = (const uint8_t *)src;
  unsigned long i;
  for (i = 0; i < count; i++) {
    out[i] = in[i];
  }
  return dst;
}

IMPORT("natcatty_host_v1", "data_provider_respond")
extern int32_t data_provider_respond(uint64_t request_id, const uint8_t *ptr, uint32_t len);
IMPORT("natcatty_host_v1", "data_provider_cancelled")
extern int32_t data_provider_cancelled(uint64_t request_id);
IMPORT("natcatty_host_v1", "now_ns")
extern int64_t now_ns(void);

/* Stable method ids from the generated IDL. */
#define METHOD_OPEN 1u
#define METHOD_CLOSE 2u
#define METHOD_PING 3u
#define METHOD_CAPABILITIES 4u
#define METHOD_QUERY 5u
#define METHOD_FETCH 6u
#define METHOD_CANCEL 7u
#define METHOD_COMMAND 8u
#define METHOD_BEGIN 9u
#define METHOD_COMMIT 10u
#define METHOD_ROLLBACK 11u
#define METHOD_METADATA 12u
#define METHOD_PLAN 13u

#define REQUEST_CAP 65536u
#define RESPONSE_CAP 12288u

/* The host writes provider request payloads into guest memory through this
 * bump allocator. The sample is a reactor: activation allocates once. */
static uint8_t heap[131072];
static uint32_t heap_used;

static uint8_t request_copy[REQUEST_CAP];
static uint8_t response[RESPONSE_CAP];
static uint32_t response_len;
static uint32_t request_length;
static uint32_t open_count;
static uint32_t cancel_seen;

static uint32_t text_len(const char *text) {
  uint32_t length = 0;
  while (text[length] != '\0') {
    length++;
  }
  return length;
}

static int bytes_start_with(const uint8_t *data, uint32_t length, const char *needle) {
  uint32_t i;
  for (i = 0; i < length; i++) {
    if (needle[i] == '\0') {
      return i > 0;
    }
    if (data[i] != (uint8_t)needle[i]) {
      return 0;
    }
  }
  return needle[length] == '\0';
}

static int contains(const char *needle) {
  uint32_t length = text_len(needle);
  uint32_t i;
  if (length == 0) {
    return 1;
  }
  for (i = 0; request_copy[i] != '\0'; i++) {
    if (bytes_start_with(request_copy + i, request_length - i, needle)) {
      return 1;
    }
  }
  return 0;
}

static int text_equals(const char *left, const char *right) {
  uint32_t i = 0;
  while (left[i] != '\0' || right[i] != '\0') {
    if (left[i] != right[i]) {
      return 0;
    }
    i++;
  }
  return 1;
}

/* json_string copies the string value of "field":"value". Provider payloads
 * contain plain ASCII without escapes, so a scan is sufficient. */
static int json_string(const char *field, char *out, uint32_t cap) {
  char pattern[96];
  uint32_t name_len = text_len(field);
  uint32_t pos = 0;
  uint32_t i = 0;
  if (name_len + 5 >= sizeof(pattern)) {
    return 0;
  }
  pattern[pos++] = '"';
  for (i = 0; i < name_len; i++) {
    pattern[pos++] = field[i];
  }
  pattern[pos++] = '"';
  pattern[pos++] = ':';
  pattern[pos] = '\0';
  for (i = 0; i < request_length; i++) {
    uint32_t j;
    if (request_copy[i] != '"') {
      continue;
    }
    if (!bytes_start_with(request_copy + i + 1, request_length - i - 1, pattern + 1)) {
      continue;
    }
    j = i + 1 + pos - 1;
    while (j < request_length && request_copy[j] == ' ') {
      j++;
    }
    if (j >= request_length || request_copy[j] != '"') {
      return 0;
    }
    j++;
    pos = 0;
    while (j < request_length && request_copy[j] != '"' && pos + 1 < cap) {
      out[pos++] = (char)request_copy[j++];
    }
    out[pos] = '\0';
    return pos > 0;
  }
  return 0;
}

static int json_u32(const char *field, uint32_t *out) {
  char pattern[64];
  uint32_t name_len = text_len(field);
  uint32_t pos = 0;
  uint32_t i = 0;
  if (name_len + 4 >= sizeof(pattern)) {
    return 0;
  }
  pattern[pos++] = '"';
  for (i = 0; i < name_len; i++) {
    pattern[pos++] = field[i];
  }
  pattern[pos++] = '"';
  pattern[pos++] = ':';
  pattern[pos] = '\0';
  for (i = 0; i < request_length; i++) {
    uint32_t j;
    uint32_t value = 0;
    int digits = 0;
    if (request_copy[i] != '"' ||
        !bytes_start_with(request_copy + i + 1, request_length - i - 1, pattern + 1)) {
      continue;
    }
    j = i + 1 + pos - 1;
    while (j < request_length && request_copy[j] == ' ') {
      j++;
    }
    while (j < request_length && request_copy[j] >= '0' && request_copy[j] <= '9') {
      value = value * 10u + (uint32_t)(request_copy[j] - '0');
      digits = 1;
      j++;
    }
    if (digits) {
      *out = value;
      return 1;
    }
    return 0;
  }
  return 0;
}

static uint32_t resp_text(const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && response_len + 1 < RESPONSE_CAP) {
    response[response_len++] = (uint8_t)text[i++];
  }
  return response_len;
}

static uint32_t resp_u32(uint32_t value) {
  char digits[16];
  uint32_t count = 0;
  uint32_t i;
  if (value == 0) {
    return resp_text("0");
  }
  while (value > 0 && count < sizeof(digits)) {
    digits[count++] = (char)('0' + (value % 10u));
    value /= 10u;
  }
  for (i = 0; i < count; i++) {
    resp_text((char[]){digits[count - 1 - i], '\0'});
  }
  return response_len;
}

static int32_t reply(uint64_t request_id) {
  return data_provider_respond(request_id, response, response_len);
}

static int32_t reply_text(uint64_t request_id, const char *text) {
  response_len = 0;
  resp_text(text);
  return reply(request_id);
}

static int32_t reply_error(uint64_t request_id, const char *status, const char *message) {
  response_len = 0;
  resp_text("{\"status\":\"");
  resp_text(status);
  resp_text("\",\"message\":\"");
  resp_text(message);
  resp_text("\"}");
  return reply(request_id);
}

static void reply_open(uint64_t request_id) {
  if (contains("\"test\":true")) {
    reply_text(request_id,
               "{\"status\":\"ok\",\"serverIdentity\":\"sample-warehouse\","
               "\"authResult\":\"accepted\"}");
    return;
  }
  open_count++;
  response_len = 0;
  resp_text("{\"status\":\"ok\",\"connection\":");
  resp_u32(open_count);
  resp_text("}");
  reply(request_id);
}

static const char ROWS_PREFIX[] =
    "{\"status\":\"ok\",\"query\":1,\"result\":{\"kind\":\"table\",\"table\":{"
    "\"columns\":[{\"name\":\"n\",\"type\":\"int\"}],\"rows\":["
    "[{\"kind\":\"int\",\"int\":\"1\"}],"
    "[{\"kind\":\"int\",\"int\":\"2\"}],"
    "[{\"kind\":\"int\",\"int\":\"3\"}]]},\"more\":true,\"class\":\"read\"}}";

static void reply_query(uint64_t request_id) {
  char query[64];
  if (!json_string("query", query, sizeof(query))) {
    reply_error(request_id, "invalid", "query is required");
    return;
  }
  if (text_equals(query, "typed")) {
    reply_text(request_id,
               "{\"status\":\"ok\",\"result\":{\"kind\":\"table\",\"table\":{"
               "\"columns\":[{\"name\":\"id\",\"type\":\"int\"},"
               "{\"name\":\"amount\",\"type\":\"decimal\"},"
               "{\"name\":\"label\",\"type\":\"string\"}],\"rows\":[["
               "{\"kind\":\"int\",\"int\":\"9007199254740993\"},"
               "{\"kind\":\"decimal\",\"decimal\":\"1234567890.000000000000000001\"},"
               "{\"kind\":\"string\",\"string\":\"ok\"}]]},"
               "\"class\":\"read\"}}");
    return;
  }
  if (text_equals(query, "rows")) {
    reply_text(request_id, ROWS_PREFIX);
    return;
  }
  if (text_equals(query, "cancel")) {
    reply_text(request_id,
               "{\"status\":\"ok\",\"query\":2,\"result\":{\"kind\":\"table\",\"table\":{"
               "\"columns\":[{\"name\":\"n\",\"type\":\"int\"}],\"rows\":["
               "[{\"kind\":\"int\",\"int\":\"1\"}],"
               "[{\"kind\":\"int\",\"int\":\"2\"}]]},\"more\":true,\"class\":\"read\"}}");
    return;
  }
  if (text_equals(query, "write")) {
    reply_text(request_id,
               "{\"status\":\"ok\",\"result\":{\"kind\":\"summary\","
               "\"summary\":{\"affectedRows\":1},\"class\":\"write\"}}");
    return;
  }
  reply_text(request_id,
             "{\"status\":\"ok\",\"result\":{\"kind\":\"summary\","
             "\"summary\":{},\"class\":\"read\"}}");
}

static void reply_fetch(uint64_t request_id) {
  uint32_t query = 0;
  if (!json_u32("query", &query)) {
    reply_error(request_id, "invalid", "query token is required");
    return;
  }
  if (query == 2) {
    /* A real provider polls the host's cooperative cancellation import while
     * it works. The host sets it when the caller's context ends; only then
     * does this batch become available. Delivered rows are marked partial by
     * the host, not trusted from the guest. */
    while (cancel_seen == 0 && !data_provider_cancelled(request_id)) {
      (void)now_ns();
    }
    reply_text(request_id,
               "{\"status\":\"ok\",\"result\":{\"kind\":\"table\",\"table\":{"
               "\"columns\":[{\"name\":\"n\",\"type\":\"int\"}],\"rows\":["
               "[{\"kind\":\"int\",\"int\":\"10\"}],"
               "[{\"kind\":\"int\",\"int\":\"11\"}]]},\"more\":true,\"class\":\"read\"}}");
    return;
  }
  if (query == 1) {
    reply_text(request_id, ROWS_PREFIX);
    return;
  }
  reply_text(request_id,
             "{\"status\":\"ok\",\"result\":{\"kind\":\"summary\","
             "\"summary\":{},\"class\":\"read\"}}");
}

EXPORT("natcatty_data_provider_request")
int32_t natcatty_data_provider_request(uint64_t request_id, int32_t method,
                                       const uint8_t *ptr, uint32_t len) {
  if (len >= REQUEST_CAP) {
    return reply_error(request_id, "limit", "request is too large");
  }
  request_length = len;
  if (ptr != 0 && len > 0) {
    memcpy(request_copy, ptr, len);
  }
  request_copy[len] = 0;
  switch ((uint32_t)method) {
    case METHOD_OPEN:
      reply_open(request_id);
      return 0;
    case METHOD_CLOSE:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_PING:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_CAPABILITIES:
      return reply_text(request_id,
                        "{\"status\":\"ok\",\"capabilities\":{"
                        "\"family\":\"tabular\",\"features\":[\"transactions\","
                        "\"metadata\",\"cancellation\",\"plans\",\"testing\"],"
                        "\"maxConnections\":1}}");
    case METHOD_QUERY:
      reply_query(request_id);
      return 0;
    case METHOD_FETCH:
      reply_fetch(request_id);
      return 0;
    case METHOD_CANCEL:
      cancel_seen = 1;
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_COMMAND:
      return reply_text(request_id,
                        "{\"status\":\"ok\",\"result\":{\"kind\":\"summary\","
                        "\"summary\":{\"affectedRows\":1},\"class\":\"read\"}}");
    case METHOD_BEGIN:
      return reply_text(request_id,
                        "{\"status\":\"ok\",\"transaction\":21}");
    case METHOD_COMMIT:
    case METHOD_ROLLBACK:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_METADATA:
      return reply_text(request_id,
                        "{\"status\":\"ok\",\"metadata\":{\"nodes\":[{"
                        "\"kind\":\"collection\",\"name\":\"tables\","
                        "\"path\":\"tables\"}]}}");
    case METHOD_PLAN:
      return reply_text(request_id,
                        "{\"status\":\"ok\",\"result\":{\"kind\":\"plan\","
                        "\"plan\":{\"text\":\"sample scan\"},\"class\":\"read\"}}");
    default:
      return reply_error(request_id, "unsupported", "unknown data method");
  }
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }
EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  uint32_t ptr = (uint32_t)heap + heap_used;
  heap_used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr,
                                                       uint32_t command_len) {
  (void)command_ptr;
  (void)command_len;
  return 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
