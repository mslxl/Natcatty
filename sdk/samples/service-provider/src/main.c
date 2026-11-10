/*
 * Cross-plugin service provider sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It provides one service
 * under its own prefix:
 *
 *   plugin.acme.service-provider.echo   v1.2.0, read access
 *     echo            request/response with a declared args and result schema
 *     download        provider-to-consumer stream (three chunks, then close)
 *     upload          consumer-to-provider stream drained by a continuation
 *     duplex          both directions mirrored back to the consumer
 *     notify          emits three ordered tick events
 *     register_extra  dynamically registers plugin.acme.service-provider.extra
 *     unregister_extra removes the dynamic declaration again
 *
 * It also consumes a consumer-owned callback service:
 *
 *   plugin.acme.service-consumer.callback   optional injection
 *
 * The `callback` command invokes that service while the consumer is idle,
 * demonstrating callback-by-service without a synchronous re-entrant call.
 * The host calls the provider only when it is ACTIVE; the sample never relies
 * on a hidden reverse channel.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned long long uint64_t;
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

unsigned long strlen(const char *text) {
  unsigned long length = 0;
  while (text[length] != '\0') {
    length++;
  }
  return length;
}

IMPORT("natcatty_host_v1", "log") extern void host_log(int32_t level, const uint8_t *ptr, int32_t len);
IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);
IMPORT("natcatty_host_v1", "service_respond") extern int32_t host_service_respond(uint64_t request_id, const uint8_t *ptr, int32_t len);

IMPORT("natcatty:svc/services_v1", "call") extern int32_t services_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

/* Stable method ids from the generated IDL. */
#define SVC_INVOKE 1u
#define SVC_STREAM_OPEN 2u
#define SVC_STREAM_READ 3u
#define SVC_STREAM_WRITE 4u
#define SVC_STREAM_CLOSE 5u
#define SVC_SUBSCRIBE 6u
#define SVC_POLL 7u
#define SVC_UNSUBSCRIBE 8u
#define SVC_REGISTER 9u
#define SVC_UNREGISTER 10u
#define SVC_PROVIDERS 11u
#define SVC_STATUS 12u
#define SVC_EMIT 13u

#define SERVICE_ECHO "plugin.acme.service-provider.echo"
#define SERVICE_EXTRA "plugin.acme.service-provider.extra"
#define SERVICE_CALLBACK "plugin.acme.service-consumer.callback"

#define RESPONSE_CAP 16384u
#define REQUEST_CAP 16384u
#define REPORT_CAP 8192u
#define CALL_CAP 4096u

/* Base64 of the sample chunks. The guest never decodes chunk bytes: it
 * forwards the base64 text the broker already validated. */
#define CHUNK_1_B64 "Y2h1bmstMQ==" /* "chunk-1", 7 bytes */
#define CHUNK_2_B64 "Y2h1bmstMg==" /* "chunk-2", 7 bytes */
#define CHUNK_3_B64 "Y2h1bmstMw==" /* "chunk-3", 7 bytes */

static uint8_t heap[131072];
static uint32_t heap_used;
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];
static char request_copy[REQUEST_CAP];
static char call_request[CALL_CAP];

static uint32_t text_len(const char *text) {
  uint32_t length = 0;
  while (text[length] != '\0') {
    length++;
  }
  return length;
}

static int text_eq(const char *left, const char *right) {
  uint32_t i = 0;
  while (left[i] != '\0' && right[i] != '\0') {
    if (left[i] != right[i]) {
      return 0;
    }
    i++;
  }
  return left[i] == right[i];
}

static void text_copy(char *dst, uint32_t cap, const char *src) {
  uint32_t i = 0;
  while (src[i] != '\0' && i + 1 < cap) {
    dst[i] = src[i];
    i++;
  }
  dst[i] = '\0';
}

/* json_field finds `"field":"value"` and copies value. The sample payloads
 * contain plain ASCII with no escapes, so a scan is sufficient. */
static int json_field(const char *json, const char *field, char *out, uint32_t cap) {
  char pattern[96];
  uint32_t name_len = text_len(field);
  uint32_t i = 0;
  uint32_t pos = 0;
  if (name_len + 8 >= sizeof(pattern)) {
    return 0;
  }
  pattern[pos++] = '"';
  for (i = 0; i < name_len; i++) {
    pattern[pos++] = field[i];
  }
  pattern[pos++] = '"';
  pattern[pos++] = ':';
  pattern[pos++] = '"';
  pattern[pos] = '\0';

  i = 0;
  while (json[i] != '\0') {
    uint32_t j = 0;
    while (pattern[j] != '\0' && json[i + j] == pattern[j]) {
      j++;
    }
    if (pattern[j] == '\0') {
      uint32_t start = i + j;
      uint32_t k = 0;
      while (json[start] != '\0' && json[start] != '"' && k + 1 < cap) {
        out[k++] = json[start++];
      }
      out[k] = '\0';
      return 1;
    }
    i++;
  }
  return 0;
}

static int json_u64(const char *json, const char *field, uint64_t *out) {
  char pattern[96];
  uint32_t name_len = text_len(field);
  uint32_t i = 0;
  uint32_t pos = 0;
  if (name_len + 8 >= sizeof(pattern)) {
    return 0;
  }
  pattern[pos++] = '"';
  for (i = 0; i < name_len; i++) {
    pattern[pos++] = field[i];
  }
  pattern[pos++] = '"';
  pattern[pos++] = ':';
  pattern[pos] = '\0';

  i = 0;
  while (json[i] != '\0') {
    uint32_t j = 0;
    while (pattern[j] != '\0' && json[i + j] == pattern[j]) {
      j++;
    }
    if (pattern[j] == '\0') {
      uint64_t value = 0;
      uint32_t start = i + j;
      if (json[start] < '0' || json[start] > '9') {
        return 0;
      }
      while (json[start] >= '0' && json[start] <= '9') {
        value = value * 10u + (uint64_t)(json[start] - '0');
        start++;
      }
      *out = value;
      return 1;
    }
    i++;
  }
  return 0;
}

static int json_true(const char *json, const char *field) {
  char pattern[96];
  uint32_t name_len = text_len(field);
  uint32_t i = 0;
  uint32_t pos = 0;
  if (name_len + 10 >= sizeof(pattern)) {
    return 0;
  }
  pattern[pos++] = '"';
  for (i = 0; i < name_len; i++) {
    pattern[pos++] = field[i];
  }
  pattern[pos++] = '"';
  pattern[pos++] = ':';
  pattern[pos++] = 't';
  pattern[pos++] = 'r';
  pattern[pos++] = 'u';
  pattern[pos++] = 'e';
  pattern[pos] = '\0';
  i = 0;
  while (json[i] != '\0') {
    uint32_t j = 0;
    while (pattern[j] != '\0' && json[i + j] == pattern[j]) {
      j++;
    }
    if (pattern[j] == '\0') {
      return 1;
    }
    i++;
  }
  return 0;
}

static int response_contains(const char *needle) {
  const char *text = (const char *)response;
  uint32_t length = text_len(needle);
  uint32_t i = 0;
  if (length == 0) {
    return 1;
  }
  while (text[i] != '\0') {
    uint32_t j = 0;
    while (j < length && text[i + j] == needle[j]) {
      j++;
    }
    if (j == length) {
      return 1;
    }
    i++;
  }
  return 0;
}

/* put appends text to a bounded buffer and keeps it NUL terminated. */
static uint32_t put(char *buffer, uint32_t cap, uint32_t pos, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = text[i++];
  }
  buffer[pos] = '\0';
  return pos;
}

static uint32_t put_u64(char *buffer, uint32_t cap, uint32_t pos, uint64_t value) {
  char digits[20];
  uint32_t count = 0;
  if (value == 0) {
    digits[count++] = '0';
  }
  while (value > 0) {
    digits[count++] = (char)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0 && pos + 1 < cap) {
    buffer[pos++] = digits[--count];
  }
  buffer[pos] = '\0';
  return pos;
}

static int32_t call_services(uint32_t method, const char *request, uint32_t length) {
  uint32_t i;
  for (i = 0; i < RESPONSE_CAP; i++) {
    response[i] = 0;
  }
  return services_call(method, (const uint8_t *)request, length, response, RESPONSE_CAP - 1u);
}

static int32_t respond(uint64_t request_id, const char *body, uint32_t length) {
  return host_service_respond(request_id, (const uint8_t *)body, (int32_t)length) < 0 ? -1 : 0;
}

static int32_t respond_code(uint64_t request_id, const char *code, const char *message) {
  uint32_t pos = 0;
  report[pos++] = '{';
  report[pos] = '\0';
  pos = put((char *)report, REPORT_CAP, pos, "\"code\":\"");
  pos = put((char *)report, REPORT_CAP, pos, code);
  report[pos++] = '"';
  report[pos] = '\0';
  if (message != 0 && message[0] != '\0') {
    pos = put((char *)report, REPORT_CAP, pos, ",\"message\":\"");
    pos = put((char *)report, REPORT_CAP, pos, message);
    report[pos++] = '"';
    report[pos] = '\0';
  }
  pos = put((char *)report, REPORT_CAP, pos, "}");
  return respond(request_id, (const char *)report, pos);
}

/* respond_data wraps one JSON object in the success envelope. */
static int32_t respond_data(uint64_t request_id, const char *data) {
  uint32_t pos = 0;
  report[pos++] = '{';
  report[pos] = '\0';
  pos = put((char *)report, REPORT_CAP, pos, "\"code\":\"ok\",\"data\":");
  pos = put((char *)report, REPORT_CAP, pos, data);
  pos = put((char *)report, REPORT_CAP, pos, "}");
  return respond(request_id, (const char *)report, pos);
}

/* relay_response forwards a services.* envelope unchanged. The broker result
 * envelope is already a valid provider response envelope. */
static int32_t relay_response(uint64_t request_id) {
  const char *text = (const char *)response;
  return respond(request_id, text, text_len(text));
}

static int32_t handle_echo(uint64_t request_id) {
  char text[1024];
  char data[1200];
  uint32_t pos = 0;
  if (!json_field(request_copy, "text", text, sizeof(text))) {
    return respond_code(request_id, "invalid-argument", "text is required");
  }
  pos = put(data, sizeof(data), 0, "{\"text\":\"echo:");
  pos = put(data, sizeof(data), pos, text);
  pos = put(data, sizeof(data), pos, "\"}");
  return respond_data(request_id, data);
}

static int32_t handle_download(uint64_t request_id) {
  char handle[32];
  uint32_t pos;
  if (!json_field(request_copy, "handle", handle, sizeof(handle))) {
    return respond_code(request_id, "invalid-argument", "stream handle is required");
  }
  pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\",\"chunk_b64\":\"" CHUNK_1_B64 "\"}");
  if (call_services(SVC_STREAM_WRITE, call_request, pos) != 0 ||
      !response_contains("\"code\":\"ok\"")) {
    return relay_response(request_id);
  }
  pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\",\"chunk_b64\":\"" CHUNK_2_B64 "\"}");
  if (call_services(SVC_STREAM_WRITE, call_request, pos) != 0 ||
      !response_contains("\"code\":\"ok\"")) {
    return relay_response(request_id);
  }
  pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\",\"chunk_b64\":\"" CHUNK_3_B64 "\"}");
  if (call_services(SVC_STREAM_WRITE, call_request, pos) != 0 ||
      !response_contains("\"code\":\"ok\"")) {
    return relay_response(request_id);
  }
  pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\"}");
  if (call_services(SVC_STREAM_CLOSE, call_request, pos) != 0 ||
      !response_contains("\"code\":\"ok\"")) {
    return relay_response(request_id);
  }
  return respond_data(request_id, "{\"chunks\":3,\"bytes\":21}");
}

/* drain_stream reads up to 32 chunks from the provider endpoint. It returns
 * the number of chunks and bytes, or -1 after relaying a failure. */
static int32_t drain_stream(uint64_t request_id, char *handle, int mirror,
                            uint32_t *chunks, uint32_t *bytes) {
  uint32_t i;
  for (i = 0; i < 32; i++) {
    char chunk[1024];
    uint64_t length = 0;
    uint32_t pos;
    pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
    pos = put(call_request, CALL_CAP, pos, handle);
    pos = put(call_request, CALL_CAP, pos, "\"}");
    if (call_services(SVC_STREAM_READ, call_request, pos) != 0 ||
        !response_contains("\"code\":\"ok\"")) {
      relay_response(request_id);
      return -1;
    }
    if (json_true((const char *)response, "again") ||
        json_true((const char *)response, "end")) {
      return 0;
    }
    if (!json_field((const char *)response, "chunk_b64", chunk, sizeof(chunk))) {
      respond_code(request_id, "internal", "stream chunk is missing");
      return -1;
    }
    if (json_u64((const char *)response, "bytes", &length)) {
      *bytes += (uint32_t)length;
    }
    *chunks += 1;
    if (mirror) {
      pos = put(call_request, CALL_CAP, 0, "{\"handle\":\"");
      pos = put(call_request, CALL_CAP, pos, handle);
      pos = put(call_request, CALL_CAP, pos, "\",\"chunk_b64\":\"");
      pos = put(call_request, CALL_CAP, pos, chunk);
      pos = put(call_request, CALL_CAP, pos, "\"}");
      if (call_services(SVC_STREAM_WRITE, call_request, pos) != 0 ||
          !response_contains("\"code\":\"ok\"")) {
        relay_response(request_id);
        return -1;
      }
    }
  }
  return 0;
}

static int32_t handle_stream_continuation(uint64_t request_id, int mirror) {
  char handle[32];
  char data[160];
  uint32_t chunks = 0;
  uint32_t bytes = 0;
  uint32_t pos;
  if (!json_field(request_copy, "handle", handle, sizeof(handle))) {
    return respond_code(request_id, "invalid-argument", "stream handle is required");
  }
  if (drain_stream(request_id, handle, mirror, &chunks, &bytes) != 0) {
    return 0;
  }
  pos = put(data, sizeof(data), 0, "{\"chunks\":");
  pos = put_u64(data, sizeof(data), pos, chunks);
  pos = put(data, sizeof(data), pos, ",\"bytes\":");
  pos = put_u64(data, sizeof(data), pos, bytes);
  pos = put(data, sizeof(data), pos, "}");
  return respond_data(request_id, data);
}

static int32_t handle_notify(uint64_t request_id) {
  uint32_t sequence;
  for (sequence = 1; sequence <= 3; sequence++) {
    uint32_t pos = 0;
    pos = put(call_request, CALL_CAP, pos, "{\"service\":\"" SERVICE_ECHO "\",\"event\":\"tick\",\"payload\":{\"kind\":\"tick\",\"sequence\":");
    pos = put_u64(call_request, CALL_CAP, pos, sequence);
    pos = put(call_request, CALL_CAP, pos, "}}");
    if (call_services(SVC_EMIT, call_request, pos) != 0 ||
        !response_contains("\"code\":\"ok\"")) {
      return relay_response(request_id);
    }
  }
  return respond_data(request_id, "{\"emitted\":3}");
}

static int32_t handle_register_extra(uint64_t request_id) {
  static const char request[] =
      "{\"service\":\"" SERVICE_EXTRA "\",\"version\":\"1.1.0\","
      "\"access\":\"read\",\"methods\":[{\"name\":\"ping\","
      "\"encoding\":\"json\"}]}";
  if (call_services(SVC_REGISTER, request, sizeof(request) - 1) != 0) {
    return respond_code(request_id, "internal", "services.register transport failure");
  }
  return relay_response(request_id);
}

static int32_t handle_unregister_extra(uint64_t request_id) {
  static const char request[] = "{\"service\":\"" SERVICE_EXTRA "\"}";
  if (call_services(SVC_UNREGISTER, request, sizeof(request) - 1) != 0) {
    return respond_code(request_id, "internal", "services.unregister transport failure");
  }
  return relay_response(request_id);
}

static int32_t handle_callback_command(void) {
  static const char request[] =
      "{\"service\":\"" SERVICE_CALLBACK "\",\"method\":\"ping\","
      "\"payload\":{\"token\":\"cb-1\"}}";
  char value[256];
  uint32_t pos = 0;
  int32_t status;
  status = call_services(SVC_INVOKE, request, sizeof(request) - 1);
  pos = put((char *)report, REPORT_CAP, pos, "callback code=");
  if (status != 0) {
    pos = put((char *)report, REPORT_CAP, pos, "transport:");
    pos = put_u64((char *)report, REPORT_CAP, pos, (uint64_t)(-status));
  } else if (json_field((const char *)response, "code", value, sizeof(value))) {
    pos = put((char *)report, REPORT_CAP, pos, value);
  } else {
    pos = put((char *)report, REPORT_CAP, pos, "unknown");
  }
  if (json_field((const char *)response, "permission", value, sizeof(value))) {
    pos = put((char *)report, REPORT_CAP, pos, " permission=");
    pos = put((char *)report, REPORT_CAP, pos, value);
  }
  if (json_field((const char *)response, "token", value, sizeof(value))) {
    pos = put((char *)report, REPORT_CAP, pos, " token=");
    pos = put((char *)report, REPORT_CAP, pos, value);
  }
  if (json_field((const char *)response, "consumer", value, sizeof(value))) {
    pos = put((char *)report, REPORT_CAP, pos, " consumer=");
    pos = put((char *)report, REPORT_CAP, pos, value);
  }
  pos = put((char *)report, REPORT_CAP, pos, "\ncallback.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

static int command_contains(uint32_t command_ptr, uint32_t command_len, const char *needle) {
  const uint8_t *command = (const uint8_t *)command_ptr;
  uint32_t length = text_len(needle);
  uint32_t i;
  if (length == 0) {
    return 1;
  }
  if (command_len < length) {
    return 0;
  }
  for (i = 0; i + length <= command_len; i++) {
    uint32_t j = 0;
    while (j < length && command[i + j] == (uint8_t)needle[j]) {
      j++;
    }
    if (j == length) {
      return 1;
    }
  }
  return 0;
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
  if (command_contains(command_ptr, command_len, "callback")) {
    return handle_callback_command();
  }
  return 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }

EXPORT("natcatty_service_request") int32_t natcatty_service_request(uint64_t request_id,
                                                                    uint32_t ptr,
                                                                    uint32_t len) {
  char method[64];
  uint32_t i;
  if (len >= REQUEST_CAP) {
    return respond_code(request_id, "invalid-argument", "request is too large");
  }
  for (i = 0; i < len; i++) {
    request_copy[i] = (char)((const uint8_t *)ptr)[i];
  }
  request_copy[len] = '\0';
  if (!json_field(request_copy, "method", method, sizeof(method))) {
    return respond_code(request_id, "invalid-argument", "method is required");
  }
  if (text_eq(method, "echo")) {
    return handle_echo(request_id);
  }
  if (text_eq(method, "download")) {
    return handle_download(request_id);
  }
  if (text_eq(method, "upload")) {
    return handle_stream_continuation(request_id, 0);
  }
  if (text_eq(method, "duplex")) {
    return handle_stream_continuation(request_id, 1);
  }
  if (text_eq(method, "notify")) {
    return handle_notify(request_id);
  }
  if (text_eq(method, "register_extra")) {
    return handle_register_extra(request_id);
  }
  if (text_eq(method, "unregister_extra")) {
    return handle_unregister_extra(request_id);
  }
  return respond_code(request_id, "unsupported", "unknown method");
}
