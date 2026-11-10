/*
 * Cross-plugin service consumer sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It optionally injects
 * the provider sample's service:
 *
 *   plugin.acme.service-provider.echo   optional, ^1.0
 *
 * The `status` command reports the optional binding, including the unbound
 * state and its typed reason. The `selftest` command drives request/response,
 * download, upload, and duplex streams, declares and polls a filtered event
 * subscription, and exercises dynamic registration through the provider.
 *
 * The plugin also provides a consumer-owned callback service:
 *
 *   plugin.acme.service-consumer.callback   read access, method ping
 *
 * The provider injects and invokes it, demonstrating callback-by-service
 * without a synchronous re-entrant call.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned long long uint64_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

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

#define RESPONSE_CAP 16384u
#define REPORT_CAP 16384u
#define CALL_CAP 4096u
#define CALLBACK_CAP 2048u

#define ALPHA_B64 "YWxwaGE=" /* "alpha", 5 bytes */
#define BETA_B64 "YmV0YQ=="  /* "beta", 4 bytes */
#define ONE_B64 "b25l"                  /* "one", 3 bytes */
#define TWO_B64 "dHdv"                  /* "two", 3 bytes */

static uint8_t heap[131072];
static uint32_t heap_used;
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];
static uint8_t callback_response[CALLBACK_CAP];
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

static uint32_t count_text(const char *json, const char *needle) {
  uint32_t length = text_len(needle);
  uint32_t count = 0;
  uint32_t i = 0;
  if (length == 0) {
    return 0;
  }
  while (json[i] != '\0') {
    uint32_t j = 0;
    while (j < length && json[i + j] == needle[j]) {
      j++;
    }
    if (j == length) {
      count++;
      i += length;
      continue;
    }
    i++;
  }
  return count;
}

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

/* r_* append to the report buffer. */
static uint32_t r_text(uint32_t pos, const char *text) {
  return put((char *)report, REPORT_CAP, pos, text);
}

static uint32_t r_bytes(uint32_t pos, const uint8_t *data, uint32_t length) {
  uint32_t i;
  for (i = 0; i < length && pos + 1 < REPORT_CAP; i++) {
    report[pos++] = data[i];
  }
  report[pos] = '\0';
  return pos;
}

static uint32_t r_u64(uint32_t pos, uint64_t value) {
  return put_u64((char *)report, REPORT_CAP, pos, value);
}

static uint32_t r_code(uint32_t pos, int32_t status) {
  char value[64];
  if (status != 0) {
    pos = r_text(pos, "transport:");
    return r_u64(pos, (uint64_t)(-status));
  }
  if (json_field((const char *)response, "code", value, sizeof(value))) {
    return r_text(pos, value);
  }
  return r_text(pos, "unknown");
}

static uint32_t r_permission(uint32_t pos) {
  char value[128];
  if (json_field((const char *)response, "permission", value, sizeof(value))) {
    pos = r_text(pos, " permission=");
    pos = r_text(pos, value);
  }
  return pos;
}

static uint32_t r_field(uint32_t pos, const char *field) {
  char value[256];
  if (json_field((const char *)response, field, value, sizeof(value))) {
    pos = r_text(pos, " ");
    pos = r_text(pos, field);
    pos = r_text(pos, "=");
    pos = r_text(pos, value);
  }
  return pos;
}

/* invoke_method builds one services.invoke body and runs it. stream is an
 * empty string for an ordinary call or the consumer stream handle for an
 * upload/duplex continuation. */
static int32_t invoke_method(const char *method, const char *payload, const char *stream) {
  uint32_t pos = 0;
  pos = put(call_request, CALL_CAP, pos, "{\"service\":\"" SERVICE_ECHO "\",\"method\":\"");
  pos = put(call_request, CALL_CAP, pos, method);
  pos = put(call_request, CALL_CAP, pos, "\"");
  if (stream != 0 && stream[0] != '\0') {
    pos = put(call_request, CALL_CAP, pos, ",\"stream\":\"");
    pos = put(call_request, CALL_CAP, pos, stream);
    pos = put(call_request, CALL_CAP, pos, "\"");
  }
  pos = put(call_request, CALL_CAP, pos, ",\"payload\":");
  pos = put(call_request, CALL_CAP, pos, payload);
  pos = put(call_request, CALL_CAP, pos, "}");
  return call_services(SVC_INVOKE, call_request, pos);
}

static int32_t open_stream(const char *method, const char *direction) {
  uint32_t pos = 0;
  pos = put(call_request, CALL_CAP, pos, "{\"service\":\"" SERVICE_ECHO "\",\"method\":\"");
  pos = put(call_request, CALL_CAP, pos, method);
  pos = put(call_request, CALL_CAP, pos, "\",\"direction\":\"");
  pos = put(call_request, CALL_CAP, pos, direction);
  pos = put(call_request, CALL_CAP, pos, "\"}");
  return call_services(SVC_STREAM_OPEN, call_request, pos);
}

static int32_t write_chunk(const char *handle, const char *b64) {
  uint32_t pos = 0;
  pos = put(call_request, CALL_CAP, pos, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\",\"chunk_b64\":\"");
  pos = put(call_request, CALL_CAP, pos, b64);
  pos = put(call_request, CALL_CAP, pos, "\"}");
  return call_services(SVC_STREAM_WRITE, call_request, pos);
}

static int32_t close_stream(const char *handle) {
  uint32_t pos = 0;
  pos = put(call_request, CALL_CAP, pos, "{\"handle\":\"");
  pos = put(call_request, CALL_CAP, pos, handle);
  pos = put(call_request, CALL_CAP, pos, "\"}");
  return call_services(SVC_STREAM_CLOSE, call_request, pos);
}

/* read_stream drains the consumer endpoint until again or end. */
static int read_stream(const char *handle, uint32_t *chunks, uint32_t *bytes) {
  uint32_t i;
  for (i = 0; i < 32; i++) {
    uint64_t length = 0;
    uint32_t pos = 0;
    pos = put(call_request, CALL_CAP, pos, "{\"handle\":\"");
    pos = put(call_request, CALL_CAP, pos, handle);
    pos = put(call_request, CALL_CAP, pos, "\"}");
    if (call_services(SVC_STREAM_READ, call_request, pos) != 0) {
      return -1;
    }
    if (count_text((const char *)response, "\"code\":\"ok\"") == 0) {
      return -1;
    }
    if (json_true((const char *)response, "again") ||
        json_true((const char *)response, "end")) {
      return 0;
    }
    *chunks += 1;
    if (json_u64((const char *)response, "bytes", &length)) {
      *bytes += (uint32_t)length;
    }
  }
  return 0;
}

static int32_t status_line(uint32_t pos, const char *label) {
  char reason[256];
  char version[64];
  pos = r_text(pos, label);
  pos = r_text(pos, " code=");
  pos = r_code(pos, call_services(SVC_STATUS,
      "{\"service\":\"" SERVICE_ECHO "\"}", sizeof("{\"service\":\"" SERVICE_ECHO "\"}") - 1));
  if (json_true((const char *)response, "bound")) {
    pos = r_text(pos, " bound=true");
    if (json_field((const char *)response, "version", version, sizeof(version))) {
      pos = r_text(pos, " version=");
      pos = r_text(pos, version);
    }
  } else {
    pos = r_text(pos, " bound=false");
    if (json_field((const char *)response, "reason", reason, sizeof(reason))) {
      pos = r_text(pos, " reason=");
      pos = r_text(pos, reason);
    }
  }
  return r_text(pos, "\n");
}

static int32_t run_providers_command(void) {
  static const char request[] = "{\"service\":\"" SERVICE_ECHO "\"}";
  uint32_t pos = 0;
  int32_t status;
  status = call_services(SVC_PROVIDERS, request, sizeof(request) - 1);
  pos = r_text(pos, "providers.echo code=");
  pos = r_code(pos, status);
  if (status == 0) {
    pos = r_text(pos, " count=");
    pos = r_u64(pos, count_text((const char *)response, "\"plugin_id\""));
  }
  pos = r_text(pos, "\nproviders.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

static int32_t run_status_command(void) {
  uint32_t pos = 0;
  pos = status_line(pos, "status");
  pos = r_text(pos, "status.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

static int32_t run_selftest(void) {
  char handle[32];
  char value[256];
  uint32_t pos = 0;
  uint32_t chunks = 0;
  uint32_t bytes = 0;
  uint64_t count = 0;
  uint64_t gap = 0;
  int subscribed = 0;
  int32_t status;

  /* The optional injection may start unbound; the binding view reports it. */
  pos = status_line(pos, "status");

  status = invoke_method("echo", "{\"text\":\"hello\"}", "");
  pos = r_text(pos, "echo code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  if (status == 0 && count_text((const char *)response, "echo:hello") > 0) {
    pos = r_text(pos, " text=echo:hello");
  }
  pos = r_text(pos, "\n");

  status = open_stream("download", "download");
  pos = r_text(pos, "download code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  if (status == 0 && json_field((const char *)response, "handle", handle, sizeof(handle))) {
    chunks = 0;
    bytes = 0;
    if (read_stream(handle, &chunks, &bytes) == 0) {
      pos = r_text(pos, " chunks=");
      pos = r_u64(pos, chunks);
      pos = r_text(pos, " bytes=");
      pos = r_u64(pos, bytes);
    } else {
      pos = r_text(pos, " read=failed");
    }
  }
  pos = r_text(pos, "\n");

  status = open_stream("upload", "upload");
  pos = r_text(pos, "upload code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  if (status == 0 && json_field((const char *)response, "handle", handle, sizeof(handle))) {
    write_chunk(handle, ALPHA_B64);
    write_chunk(handle, BETA_B64);
    if (invoke_method("upload", "{}", handle) == 0) {
      chunks = 0;
      bytes = 0;
      json_u64((const char *)response, "chunks", &count);
      chunks = (uint32_t)count;
      json_u64((const char *)response, "bytes", &count);
      bytes = (uint32_t)count;
      pos = r_text(pos, " chunks=");
      pos = r_u64(pos, chunks);
      pos = r_text(pos, " bytes=");
      pos = r_u64(pos, bytes);
    }
    pos = r_text(pos, " close=");
    pos = r_code(pos, close_stream(handle));
  }
  pos = r_text(pos, "\n");

  status = open_stream("duplex", "duplex");
  pos = r_text(pos, "duplex code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  if (status == 0 && json_field((const char *)response, "handle", handle, sizeof(handle))) {
    write_chunk(handle, ONE_B64);
    write_chunk(handle, TWO_B64);
    if (invoke_method("duplex", "{}", handle) == 0) {
      chunks = 0;
      bytes = 0;
      if (read_stream(handle, &chunks, &bytes) == 0) {
        pos = r_text(pos, " chunks=");
        pos = r_u64(pos, chunks);
        pos = r_text(pos, " bytes=");
        pos = r_u64(pos, bytes);
      }
    }
    pos = r_text(pos, " close=");
    pos = r_code(pos, close_stream(handle));
  }
  pos = r_text(pos, "\n");

  status = call_services(SVC_SUBSCRIBE,
      "{\"service\":\"" SERVICE_ECHO "\",\"event\":\"tick\","
      "\"filter\":{\"kind\":\"tick\"}}",
      sizeof("{\"service\":\"" SERVICE_ECHO "\",\"event\":\"tick\","
             "\"filter\":{\"kind\":\"tick\"}}") - 1);
  pos = r_text(pos, "subscribe code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  if (status == 0 && json_field((const char *)response, "handle", handle, sizeof(handle))) {
    subscribed = 1;
    pos = r_text(pos, " handle=");
    pos = r_text(pos, handle);
  }
  pos = r_text(pos, "\n");

  status = invoke_method("notify", "{}", "");
  pos = r_text(pos, "notify code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  pos = r_text(pos, "\n");

  if (subscribed) {
    uint32_t poll_pos = 0;
    poll_pos = put(call_request, CALL_CAP, poll_pos, "{\"handle\":\"");
    poll_pos = put(call_request, CALL_CAP, poll_pos, handle);
    poll_pos = put(call_request, CALL_CAP, poll_pos, "\",\"limit\":16}");
    status = call_services(SVC_POLL, call_request, poll_pos);
    pos = r_text(pos, "poll code=");
    pos = r_code(pos, status);
    pos = r_permission(pos);
    if (status == 0) {
      pos = r_text(pos, " events=");
      pos = r_u64(pos, count_text((const char *)response, "\"event\":\"tick\""));
      if (json_u64((const char *)response, "gap", &gap)) {
        pos = r_text(pos, " gap=");
        pos = r_u64(pos, gap);
      }
      pos = r_text(pos, "\npoll.response=");
      pos = r_bytes(pos, response, text_len((const char *)response));
    }
    pos = r_text(pos, "\n");
  } else {
    pos = r_text(pos, "poll skipped\n");
  }

  status = call_services(SVC_PROVIDERS,
      "{\"service\":\"" SERVICE_EXTRA "\"}",
      sizeof("{\"service\":\"" SERVICE_EXTRA "\"}") - 1);
  pos = r_text(pos, "providers.extra.before code=");
  pos = r_code(pos, status);
  if (status == 0) {
    pos = r_text(pos, " count=");
    pos = r_u64(pos, count_text((const char *)response, "\"plugin_id\""));
  }
  pos = r_text(pos, "\n");

  status = invoke_method("register_extra", "{}", "");
  pos = r_text(pos, "register_extra code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  pos = r_text(pos, "\n");

  status = call_services(SVC_PROVIDERS,
      "{\"service\":\"" SERVICE_EXTRA "\"}",
      sizeof("{\"service\":\"" SERVICE_EXTRA "\"}") - 1);
  pos = r_text(pos, "providers.extra.after code=");
  pos = r_code(pos, status);
  if (status == 0) {
    pos = r_text(pos, " count=");
    pos = r_u64(pos, count_text((const char *)response, "\"plugin_id\""));
    if (json_true((const char *)response, "dynamic")) {
      pos = r_text(pos, " dynamic=true");
    }
  }
  pos = r_text(pos, "\n");

  status = invoke_method("unregister_extra", "{}", "");
  pos = r_text(pos, "unregister_extra code=");
  pos = r_code(pos, status);
  pos = r_permission(pos);
  pos = r_text(pos, "\n");

  status = call_services(SVC_PROVIDERS,
      "{\"service\":\"" SERVICE_EXTRA "\"}",
      sizeof("{\"service\":\"" SERVICE_EXTRA "\"}") - 1);
  pos = r_text(pos, "providers.extra.final code=");
  pos = r_code(pos, status);
  if (status == 0) {
    pos = r_text(pos, " count=");
    pos = r_u64(pos, count_text((const char *)response, "\"plugin_id\""));
  }
  pos = r_text(pos, "\n");

  pos = r_text(pos, "selftest.done\n");
  (void)value;
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
  if (command_contains(command_ptr, command_len, "selftest")) {
    return run_selftest();
  }
  if (command_contains(command_ptr, command_len, "providers")) {
    return run_providers_command();
  }
  if (command_contains(command_ptr, command_len, "status")) {
    return run_status_command();
  }
  return run_status_command();
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }

/* natcatty_service_request answers the provider's callback-by-service calls.
 * The provider invokes the consumer-owned service; the consumer answers
 * through service_respond like any other provider. */
EXPORT("natcatty_service_request") int32_t natcatty_service_request(uint64_t request_id,
                                                                    uint32_t ptr,
                                                                    uint32_t len) {
  char token[128];
  uint32_t pos = 0;
  uint32_t i;
  for (i = 0; i < len && i + 1 < CALLBACK_CAP; i++) {
    callback_response[i] = ((const uint8_t *)ptr)[i];
  }
  callback_response[len < CALLBACK_CAP ? len : CALLBACK_CAP - 1] = 0;
  if (!json_field((const char *)callback_response, "token", token, sizeof(token))) {
    text_copy(token, sizeof(token), "none");
  }
  pos = put((char *)callback_response, CALLBACK_CAP, 0, "{\"code\":\"ok\",\"data\":{\"token\":\"");
  pos = put((char *)callback_response, CALLBACK_CAP, pos, token);
  pos = put((char *)callback_response, CALLBACK_CAP, pos, "\",\"consumer\":\"acme.service-consumer\"}}");
  return host_service_respond(request_id, callback_response, (int32_t)pos) < 0 ? -1 : 0;
}
