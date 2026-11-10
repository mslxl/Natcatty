/*
 * loopback connection provider sample guest.
 *
 * This is a real WASM plugin built against the v1.11 provider ABI. It owns the
 * protocol x-acme-loopback and turns every Session into an echo
 * terminal: bytes the host writes to the Session come back unchanged through
 * the bounded provider stream. It never sees terminal state, dimensions
 * bookkeeping, credentials, or trust material.
 *
 * On open it drives the host-owned services:
 *   auth.begin           method "none" for the requested Host
 *   trust.verify         kind "ssh-hostkey" for the requested Host, presenting
 *                        the fixed sample host key below
 *
 * The provider only learns whether the host allowed the flow; the host keeps
 * every credential and every trust verdict. Only an explicit "trusted" verdict
 * with an ok envelope lets the open proceed: unknown material (including a
 * user-declined confirmation), changed or revoked material, and any host
 * service error all fail closed.
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

IMPORT("natcatty_host_v1", "provider_respond") extern int32_t provider_respond(uint64_t request_id, const uint8_t *ptr, uint32_t len);
IMPORT("natcatty_host_v1", "provider_cancelled") extern int32_t provider_cancelled(uint64_t request_id);
IMPORT("natcatty_host_v1", "provider_input_read") extern int32_t provider_input_read(uint32_t stream, uint8_t *ptr, uint32_t capacity);
IMPORT("natcatty_host_v1", "provider_input_credit") extern int32_t provider_input_credit(uint32_t stream);
IMPORT("natcatty_host_v1", "provider_output_write") extern int32_t provider_output_write(uint32_t stream, const uint8_t *ptr, uint32_t len);
IMPORT("natcatty_host_v1", "provider_output_credit") extern int32_t provider_output_credit(uint32_t stream);

IMPORT("natcatty:svc/auth_v1", "call") extern int32_t auth_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/trust_v1", "call") extern int32_t trust_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

/* Stable method ids from the generated IDL. */
#define METHOD_DESCRIBE 1u
#define METHOD_VALIDATE 2u
#define METHOD_PROBE 3u
#define METHOD_OPEN 4u
#define METHOD_RESIZE 5u
#define METHOD_SIGNAL 6u
#define METHOD_CLOSE 7u
#define METHOD_PUMP 8u

#define AUTH_BEGIN 1u
#define TRUST_VERIFY 1u

/* The sample presents one fixed SSH host key. It is a real ed25519 public key
 * blob (base64 of the SSH wire encoding), so the production trust confirmer can
 * classify and confirm it exactly like an SSH Session host key. The host owns
 * the fingerprint and the verdict; the guest never interprets the key. */
static const char SAMPLE_HOST_KEY[] =
    "AAAAC3NzaC1lZDI1NTE5AAAAIGpfps5bf8fgoJblrbNJe/oeii0cdpckYWK7Ixa3wQrR";

#define REQUEST_CAP 65536u
#define RESPONSE_CAP 8192u
#define CALL_CAP 4096u
#define CHUNK_CAP 4096u
#define PUMP_BUDGET 131072u

/* The host writes provider request payloads into guest memory through this
 * bump allocator. The sample is a reactor: activation allocates once. */
static uint8_t heap[65536];
static uint32_t heap_used;

static uint8_t request_copy[REQUEST_CAP];
static uint8_t response[RESPONSE_CAP];
static uint8_t call_response[CALL_CAP];
static uint8_t chunk[CHUNK_CAP];
static char call_request[CALL_CAP];
static uint32_t response_len;
static uint32_t request_length;

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

static int contains(const uint8_t *data, const char *needle) {
  uint32_t length = text_len(needle);
  uint32_t i;
  if (length == 0) {
    return 1;
  }
  for (i = 0; data[i] != '\0'; i++) {
    if (bytes_start_with(data + i, length, needle)) {
      return 1;
    }
  }
  return 0;
}

/* json_string copies the string value of "field":"value". Provider payloads
 * contain plain ASCII without escapes, so a scan is sufficient. */
static int json_string(uint32_t request_len, const char *field, char *out, uint32_t cap) {
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
  for (i = 0; i < request_len; i++) {
    uint32_t j;
    if (request_copy[i] != '"') {
      continue;
    }
    if (!bytes_start_with(request_copy + i + 1, request_len - i - 1, pattern + 1)) {
      continue;
    }
    j = i + 1 + pos - 1;
    while (j < request_len && request_copy[j] == ' ') {
      j++;
    }
    if (j >= request_len || request_copy[j] != '"') {
      return 0;
    }
    j++;
    pos = 0;
    while (j < request_len && request_copy[j] != '"' && pos + 1 < cap) {
      out[pos++] = (char)request_copy[j++];
    }
    out[pos] = '\0';
    return pos > 0;
  }
  return 0;
}

static int json_u32(uint32_t request_len, const char *field, uint32_t *out) {
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
  for (i = 0; i < request_len; i++) {
    uint32_t j;
    uint32_t value = 0;
    int digits = 0;
    if (request_copy[i] != '"' ||
        !bytes_start_with(request_copy + i + 1, request_len - i - 1, pattern + 1)) {
      continue;
    }
    j = i + 1 + pos - 1;
    while (j < request_len && request_copy[j] == ' ') {
      j++;
    }
    while (j < request_len && request_copy[j] >= '0' && request_copy[j] <= '9') {
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

static uint32_t resp_bytes(const uint8_t *data, uint32_t length) {
  uint32_t i;
  for (i = 0; i < length && response_len + 1 < RESPONSE_CAP; i++) {
    response[response_len++] = data[i];
  }
  return response_len;
}

static int32_t reply(uint64_t request_id, const uint8_t *data, uint32_t length) {
  return provider_respond(request_id, data, length);
}

static int32_t reply_text(uint64_t request_id, const char *text) {
  return reply(request_id, (const uint8_t *)text, text_len(text));
}

static int32_t reply_error(uint64_t request_id, const char *status, const char *message) {
  response_len = 0;
  resp_text("{\"status\":\"");
  resp_text(status);
  resp_text("\",\"message\":\"");
  resp_text(message);
  resp_text("\"}");
  return reply(request_id, response, response_len);
}

/* base64_encode encodes the small fixed sample values the provider returns. */
static uint32_t base64_encode(const char *data, uint32_t length, char *out, uint32_t cap) {
  static const char alphabet[] =
      "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  uint32_t i = 0;
  uint32_t pos = 0;
  while (i + 2 < length && pos + 4 < cap) {
    uint32_t value = ((uint32_t)(uint8_t)data[i] << 16) |
                     ((uint32_t)(uint8_t)data[i + 1] << 8) |
                     (uint32_t)(uint8_t)data[i + 2];
    out[pos++] = alphabet[(value >> 18) & 0x3f];
    out[pos++] = alphabet[(value >> 12) & 0x3f];
    out[pos++] = alphabet[(value >> 6) & 0x3f];
    out[pos++] = alphabet[value & 0x3f];
    i += 3;
  }
  if (i < length && pos + 4 < cap) {
    uint32_t value = (uint32_t)(uint8_t)data[i] << 16;
    out[pos++] = alphabet[(value >> 18) & 0x3f];
    if (i + 1 < length) {
      value |= (uint32_t)(uint8_t)data[i + 1] << 8;
      out[pos++] = alphabet[(value >> 12) & 0x3f];
      out[pos++] = alphabet[(value >> 6) & 0x3f];
    } else {
      out[pos++] = alphabet[(value >> 12) & 0x3f];
      out[pos++] = '=';
    }
    out[pos++] = '=';
  }
  out[pos] = '\0';
  return pos;
}

static uint32_t put_text(uint32_t pos, uint32_t cap, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < cap) {
    call_request[pos++] = text[i++];
  }
  call_request[pos] = '\0';
  return pos;
}

static void clear_call_response(void) {
  uint32_t i;
  for (i = 0; i < CALL_CAP; i++) {
    call_response[i] = 0;
  }
}

/* host_auth_begin drives the host-owned authentication service. The guest
 * asks for one method and receives only the challenge description. */
static int host_auth_begin(const char *host_id) {
  uint32_t pos = 0;
  int32_t status;
  pos = put_text(pos, CALL_CAP, "{\"host_id\":\"");
  pos = put_text(pos, CALL_CAP, host_id);
  pos = put_text(pos, CALL_CAP, "\",\"method\":\"none\"}");
  clear_call_response();
  status = auth_call(AUTH_BEGIN, (const uint8_t *)call_request, pos,
                     call_response, CALL_CAP - 1u);
  return status == 0 && contains(call_response, "\"code\":\"ok\"");
}

/* host_trust_verify asks the host to classify opaque material. The verdict is
 * host-owned and this sample never overrides it: only an explicit "trusted"
 * verdict in an ok envelope proceeds. unknown (a missing pin or a declined
 * confirmation), changed, revoked, and every host error fail the open. */
static int host_trust_verify(const char *host_id) {
  char material[160];
  uint32_t pos = 0;
  int32_t status;
  base64_encode(SAMPLE_HOST_KEY, text_len(SAMPLE_HOST_KEY), material,
                sizeof(material));
  pos = put_text(pos, CALL_CAP, "{\"host_id\":\"");
  pos = put_text(pos, CALL_CAP, host_id);
  pos = put_text(pos, CALL_CAP, "\",\"kind\":\"ssh-hostkey\",\"material\":\"");
  pos = put_text(pos, CALL_CAP, material);
  pos = put_text(pos, CALL_CAP, "\"}");
  clear_call_response();
  status = trust_call(TRUST_VERIFY, (const uint8_t *)call_request, pos,
                      call_response, CALL_CAP - 1u);
  if (status != 0 || !contains(call_response, "\"code\":\"ok\"")) {
    return 0;
  }
  return contains(call_response, "\"verdict\":\"trusted\"");
}

static int target_valid(void) {
  char target[160];
  return json_string(request_length, "target", target, sizeof(target));
}

static void reply_open_ok(uint64_t request_id) {
  char restore[64];
  base64_encode("echo-state", 10, restore, sizeof(restore));
  response_len = 0;
  resp_text("{\"status\":\"ok\",\"cols\":100,\"rows\":30,\"restore\":\"");
  resp_text(restore);
  resp_text("\"}");
  reply(request_id, response, response_len);
}

static void pump_stream(uint64_t request_id) {
  uint32_t stream = 0;
  uint32_t budget = 0;
  if (!json_u32(request_length, "stream", &stream)) {
    reply_error(request_id, "protocol-error", "stream id is required");
    return;
  }
  while (budget < PUMP_BUDGET) {
    int32_t credit = provider_output_credit(stream);
    uint32_t capacity = CHUNK_CAP;
    int32_t count;
    if (credit <= 0) {
      /* The bounded output queue is full. Stop without consuming more input;
       * the host schedules another turn when the terminal reads. */
      break;
    }
    if ((uint32_t)credit < capacity) {
      capacity = (uint32_t)credit;
    }
    count = provider_input_read(stream, chunk, capacity);
    if (count <= 0) {
      break;
    }
    if (provider_output_write(stream, chunk, (uint32_t)count) != 0) {
      reply_error(request_id, "protocol-error", "echo output did not fit");
      return;
    }
    budget += (uint32_t)count;
  }
  if (budget >= PUMP_BUDGET) {
    reply_text(request_id, "{\"status\":\"ok\",\"more\":true}");
    return;
  }
  reply_text(request_id, "{\"status\":\"ok\"}");
}

EXPORT("natcatty_provider_request")
int32_t natcatty_provider_request(uint64_t request_id, int32_t method,
                                  const uint8_t *ptr, uint32_t len) {
  if (len >= REQUEST_CAP) {
    return reply_error(request_id, "protocol-error", "request is too large");
  }
  request_length = len;
  if (ptr != 0 && len > 0) {
    memcpy(request_copy, ptr, len);
  }
  request_copy[len] = 0;
  if (provider_cancelled(request_id)) {
    return reply_error(request_id, "cancelled", "open was cancelled");
  }
  switch ((uint32_t)method) {
    case METHOD_DESCRIBE:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_VALIDATE:
      if (!target_valid()) {
        return reply_error(request_id, "protocol-error", "target is required");
      }
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_PROBE:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_OPEN: {
      char host_id[160];
      if (!target_valid()) {
        return reply_error(request_id, "protocol-error", "target is required");
      }
      if (!json_string(request_length, "hostId", host_id, sizeof(host_id))) {
        return reply_error(request_id, "protocol-error", "host id is required");
      }
      if (!host_auth_begin(host_id)) {
        return reply_error(request_id, "auth-failed", "host rejected the authentication flow");
      }
      if (!host_trust_verify(host_id)) {
        return reply_error(request_id, "host-key-changed", "host trust verification blocked");
      }
      reply_open_ok(request_id);
      return 0;
    }
    case METHOD_RESIZE:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_SIGNAL:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_CLOSE:
      return reply_text(request_id, "{\"status\":\"ok\"}");
    case METHOD_PUMP:
      pump_stream(request_id);
      return 0;
    default:
      return reply_error(request_id, "protocol-error", "unknown provider method");
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
