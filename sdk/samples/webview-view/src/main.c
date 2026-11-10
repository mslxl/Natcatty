/*
 * Web View sample guest.
 *
 * Two surfaces of one plugin share this backend:
 *
 *   - the standalone web View loads ui/index.html from the per-plugin origin
 *     natcatty-plugin://acme.webview-view/ and calls the greet and report
 *     bridge methods
 *   - the native View commits a retained tree with one embedded webview node
 *     whose src is ui/embed.html; that page calls the same bridge
 *
 * A bridge request arrives as a session-wide webview event (kind 18). The
 * guest reads the JSON envelope from the event text, answers through
 * ui.webview_respond, and never learns a host pointer. The page is not a
 * permission principal: ui.webview_respond is checked against this backend's
 * ui.webview grant, and the application verifies the calling page before the
 * event is ever queued.
 *
 * The tree commit needs the optional ui.native grant. Without it ui.apply
 * returns status=denied, the native View stays empty, and the standalone page
 * still works. Both paths are exercised by app/webview_sample_e2e_test.go.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;
typedef signed long long int64_t;
typedef unsigned long long uint64_t;

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

IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);
IMPORT("natcatty:svc/ui_v1", "call") extern int32_t ui_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

/* Stable ids from the generated IDL. */
#define UI_APPLY 8u
#define UI_LISTEN 9u
#define UI_POLL 11u
#define UI_WEBVIEW_RESPOND 12u

#define NODE_CONTAINER 0u
#define NODE_TEXT 1u
#define NODE_WEBVIEW 34u

#define OP_CREATE 0u
#define OP_SET_PROP 4u
#define OP_SET_TEXT 5u
#define OP_SET_CHILDREN 6u

#define PROP_LAYOUT 1u
#define PROP_SRC 38u

#define VALUE_INT 1u
#define VALUE_STRING 3u

#define EVENT_WEBVIEW 18u

#define LAYOUT_COLUMN 1

/* Native UI wire status values. */
#define STATUS_OK 0u
#define STATUS_DENIED 3u
/* Not a wire value: distinguishes a call that never ran from a wire status. */
#define STATUS_NOT_RUN 99

#define REQUEST_CAP 4096u
#define RESPONSE_CAP 32768u
#define REPORT_CAP 2048u
#define PAYLOAD_CAP 1024u
#define METHOD_CAP 64u
#define EVENT_FRAME 76u

static uint8_t heap[65536];
static uint32_t heap_used;
static uint8_t request[REQUEST_CAP];
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];

static uint64_t handle_root;
static uint64_t handle_label;
static uint64_t handle_webview;
static uint32_t revision;
static int32_t apply_status = STATUS_NOT_RUN;
static int32_t listen_status = STATUS_NOT_RUN;
static uint32_t handled_requests;
static uint32_t denied_requests;
static char last_method[METHOD_CAP];
static char reply_payload[PAYLOAD_CAP];

/* --- little-endian wire helpers --------------------------------------- */

static uint32_t put_u8(uint8_t *out, uint32_t pos, uint8_t value) {
  out[pos] = value;
  return pos + 1;
}

static uint32_t put_u32(uint8_t *out, uint32_t pos, uint32_t value) {
  out[pos] = (uint8_t)value;
  out[pos + 1] = (uint8_t)(value >> 8);
  out[pos + 2] = (uint8_t)(value >> 16);
  out[pos + 3] = (uint8_t)(value >> 24);
  return pos + 4;
}

static uint32_t put_u64(uint8_t *out, uint32_t pos, uint64_t value) {
  pos = put_u32(out, pos, (uint32_t)value);
  return put_u32(out, pos, (uint32_t)(value >> 32));
}

static uint32_t put_str(uint8_t *out, uint32_t pos, const char *text) {
  uint32_t length = (uint32_t)strlen(text);
  uint32_t i;
  pos = put_u32(out, pos, length);
  for (i = 0; i < length; i++) {
    out[pos++] = (uint8_t)text[i];
  }
  return pos;
}

static uint32_t read_u32(const uint8_t *in) {
  return (uint32_t)in[0] | ((uint32_t)in[1] << 8) | ((uint32_t)in[2] << 16) |
         ((uint32_t)in[3] << 24);
}

static uint64_t read_u64(const uint8_t *in) {
  return (uint64_t)read_u32(in) | ((uint64_t)read_u32(in + 4) << 32);
}

/* --- text helpers ------------------------------------------------------ */

static int text_equal(const char *a, const char *b) {
  uint32_t i = 0;
  while (a[i] != '\0' && b[i] != '\0') {
    if (a[i] != b[i]) {
      return 0;
    }
    i++;
  }
  return a[i] == b[i];
}

static void text_copy(char *dst, uint32_t cap, const char *src) {
  uint32_t i = 0;
  while (src[i] != '\0' && i + 1 < cap) {
    dst[i] = src[i];
    i++;
  }
  dst[i] = '\0';
}

/* --- bounded JSON envelope reading -------------------------------------
 *
 * The application marshals the envelope, so the sample only needs four fixed
 * fields: type, request, method, payload. The payload is page-controlled
 * text, so string decoding is bounded and handles the JSON escapes the
 * encoder can produce. */

static const uint8_t *find_field(const uint8_t *json, uint32_t len, const char *key) {
  uint32_t key_len = (uint32_t)strlen(key);
  uint32_t i;
  if (key_len == 0 || len < key_len) {
    return 0;
  }
  for (i = 0; i + key_len <= len; i++) {
    uint32_t j = 0;
    while (j < key_len && json[i + j] == (uint8_t)key[j]) {
      j++;
    }
    if (j == key_len) {
      return json + i + key_len;
    }
  }
  return 0;
}

static int hex_value(uint8_t byte) {
  if (byte >= (uint8_t)'0' && byte <= (uint8_t)'9') {
    return (int)(byte - (uint8_t)'0');
  }
  if (byte >= (uint8_t)'a' && byte <= (uint8_t)'f') {
    return 10 + (int)(byte - (uint8_t)'a');
  }
  if (byte >= (uint8_t)'A' && byte <= (uint8_t)'F') {
    return 10 + (int)(byte - (uint8_t)'A');
  }
  return -1;
}

/* decode_string reads one JSON string and returns the position after the
 * closing quote, or 0 on a malformed or oversized value. */
static uint32_t decode_string(const uint8_t *json, uint32_t len, uint32_t pos,
                              char *out, uint32_t cap) {
  uint32_t written = 0;
  if (pos >= len || json[pos] != (uint8_t)'"') {
    return 0;
  }
  pos++;
  while (pos < len) {
    uint8_t byte = json[pos++];
    if (byte == (uint8_t)'"') {
      out[written] = '\0';
      return pos;
    }
    if (byte == (uint8_t)'\\') {
      uint8_t escape;
      if (pos >= len) {
        return 0;
      }
      escape = json[pos++];
      switch (escape) {
        case (uint8_t)'"': byte = (uint8_t)'"'; break;
        case (uint8_t)'\\': byte = (uint8_t)'\\'; break;
        case (uint8_t)'/': byte = (uint8_t)'/'; break;
        case (uint8_t)'b': byte = 8; break;
        case (uint8_t)'f': byte = 12; break;
        case (uint8_t)'n': byte = (uint8_t)'\n'; break;
        case (uint8_t)'r': byte = (uint8_t)'\r'; break;
        case (uint8_t)'t': byte = (uint8_t)'\t'; break;
        case (uint8_t)'u': {
          uint32_t code = 0;
          uint32_t i;
          if (pos + 4 > len) {
            return 0;
          }
          for (i = 0; i < 4; i++) {
            int digit = hex_value(json[pos + i]);
            if (digit < 0) {
              return 0;
            }
            code = (code << 4) | (uint32_t)digit;
          }
          pos += 4;
          /* Encode one BMP code point. Surrogates are not expected in the
           * sample's fields; replace them rather than emit invalid UTF-8. */
          if (code >= 0xD800u && code <= 0xDFFFu) {
            code = 0xFFFDu;
          }
          if (code < 0x80u) {
            if (written + 1 >= cap) {
              return 0;
            }
            out[written++] = (char)code;
          } else if (code < 0x800u) {
            if (written + 2 >= cap) {
              return 0;
            }
            out[written++] = (char)(0xC0u | (code >> 6));
            out[written++] = (char)(0x80u | (code & 0x3Fu));
          } else {
            if (written + 3 >= cap) {
              return 0;
            }
            out[written++] = (char)(0xE0u | (code >> 12));
            out[written++] = (char)(0x80u | ((code >> 6) & 0x3Fu));
            out[written++] = (char)(0x80u | (code & 0x3Fu));
          }
          continue;
        }
        default:
          return 0;
      }
    }
    if (written + 1 >= cap) {
      return 0;
    }
    out[written++] = (char)byte;
  }
  return 0;
}

static int string_field(const uint8_t *json, uint32_t len, const char *key,
                        char *out, uint32_t cap) {
  const uint8_t *field = find_field(json, len, key);
  uint32_t pos;
  if (field == 0) {
    return 0;
  }
  pos = (uint32_t)(field - json);
  while (pos < len && (json[pos] == (uint8_t)' ' || json[pos] == (uint8_t)'\t')) {
    pos++;
  }
  return decode_string(json, len, pos, out, cap) != 0;
}

static int u64_field(const uint8_t *json, uint32_t len, const char *key, uint64_t *out) {
  const uint8_t *field = find_field(json, len, key);
  uint32_t pos;
  uint64_t value = 0;
  uint32_t digits = 0;
  if (field == 0) {
    return 0;
  }
  pos = (uint32_t)(field - json);
  while (pos < len && (json[pos] == (uint8_t)' ' || json[pos] == (uint8_t)'\t')) {
    pos++;
  }
  while (pos < len && json[pos] >= (uint8_t)'0' && json[pos] <= (uint8_t)'9') {
    value = value * 10u + (uint64_t)(json[pos] - (uint8_t)'0');
    pos++;
    digits++;
    if (digits > 20) {
      return 0;
    }
  }
  if (digits == 0) {
    return 0;
  }
  *out = value;
  return 1;
}

/* --- native UI calls --------------------------------------------------- */

static int32_t call_ui(uint32_t method, const uint8_t *req, uint32_t length) {
  return ui_call(method, req, length, response, RESPONSE_CAP - 1u);
}

/* response_status reads the envelope status after a successful transport
 * call. A negative transport status is kept as-is for the report. */
static int32_t response_status(int32_t transport) {
  if (transport != 0) {
    return transport;
  }
  return (int32_t)read_u32(response);
}

static uint8_t *batch_buffer;

static uint32_t add_create(uint32_t pos, uint32_t id, uint32_t kind) {
  pos = put_u8(batch_buffer, pos, OP_CREATE);
  pos = put_u32(batch_buffer, pos, id);
  return put_u32(batch_buffer, pos, kind);
}

static uint32_t add_children(uint32_t pos, uint32_t parent, const uint32_t *ids, uint32_t count) {
  uint32_t i;
  pos = put_u8(batch_buffer, pos, OP_SET_CHILDREN);
  pos = put_u64(batch_buffer, pos, (uint64_t)parent);
  pos = put_u32(batch_buffer, pos, count);
  for (i = 0; i < count; i++) {
    pos = put_u64(batch_buffer, pos, (uint64_t)ids[i]);
  }
  return pos;
}

static uint32_t add_int_prop(uint32_t pos, uint64_t node, uint32_t prop, int64_t value) {
  pos = put_u8(batch_buffer, pos, OP_SET_PROP);
  pos = put_u64(batch_buffer, pos, node);
  pos = put_u32(batch_buffer, pos, prop);
  pos = put_u32(batch_buffer, pos, VALUE_INT);
  pos = put_u64(batch_buffer, pos, (uint64_t)value);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, 0);
  return put_str(batch_buffer, pos, "");
}

static uint32_t add_string_prop(uint32_t pos, uint64_t node, uint32_t prop, const char *value) {
  pos = put_u8(batch_buffer, pos, OP_SET_PROP);
  pos = put_u64(batch_buffer, pos, node);
  pos = put_u32(batch_buffer, pos, prop);
  pos = put_u32(batch_buffer, pos, VALUE_STRING);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, 0);
  return put_str(batch_buffer, pos, value);
}

static uint32_t add_text(uint32_t pos, uint64_t node, const char *text) {
  pos = put_u8(batch_buffer, pos, OP_SET_TEXT);
  pos = put_u64(batch_buffer, pos, node);
  return put_str(batch_buffer, pos, text);
}

/* parse_created maps the handles ui.apply returned to the sample's nodes. */
static void parse_created(void) {
  uint32_t pos = 4 + 8; /* status, revision */
  uint32_t message_length;
  uint32_t count;
  uint32_t i;
  revision = read_u32(response + 4);
  message_length = read_u32(response + pos);
  pos += 4 + message_length;
  count = read_u32(response + pos);
  pos += 4;
  for (i = 0; i < count; i++) {
    uint32_t id = read_u32(response + pos);
    uint64_t handle = read_u64(response + pos + 4);
    pos += 12;
    if (id == 1) {
      handle_root = handle;
    } else if (id == 2) {
      handle_label = handle;
    } else if (id == 3) {
      handle_webview = handle;
    }
  }
}

/* apply_initial commits the native View that holds the embedded page. */
static void apply_initial(void) {
  static const uint32_t children[] = {2, 3};
  uint32_t pos = 0;
  int32_t transport;

  batch_buffer = request;
  pos = put_u64(request, pos, 1); /* root */
  pos = put_u32(request, pos, 7);  /* creates, children, layout, text, src */
  pos = add_create(pos, 1, NODE_CONTAINER);
  pos = add_create(pos, 2, NODE_TEXT);
  pos = add_create(pos, 3, NODE_WEBVIEW);
  pos = add_children(pos, 1, children, 2);
  pos = add_int_prop(pos, 1, PROP_LAYOUT, LAYOUT_COLUMN);
  pos = add_text(pos, 2, "The embedded page follows. It uses the same per-plugin origin.");
  pos = add_string_prop(pos, 3, PROP_SRC, "ui/embed.html");
  transport = call_ui(UI_APPLY, request, pos);
  apply_status = response_status(transport);
  if (apply_status == (int32_t)STATUS_OK) {
    parse_created();
  }
}

/* listen_webview subscribes to the session-wide webview event. Node 0 means
 * the whole session. The listener is served under ui.webview, so the
 * standalone page keeps working even when ui.native is not granted. */
static void listen_webview(void) {
  uint32_t pos = 0;
  int32_t transport;
  pos = put_u64(request, pos, 0);
  pos = put_u32(request, pos, EVENT_WEBVIEW);
  transport = call_ui(UI_LISTEN, request, pos);
  listen_status = response_status(transport);
}

/* --- bridge answers ---------------------------------------------------- */

/* r_* append to reply_payload, leaving room for a trailing NUL. */
static uint32_t r_text(uint32_t pos, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < PAYLOAD_CAP) {
    reply_payload[pos++] = text[i++];
  }
  reply_payload[pos] = '\0';
  return pos;
}

static uint32_t r_u32(uint32_t pos, uint32_t value) {
  uint8_t digits[10];
  uint32_t count = 0;
  if (value == 0) {
    digits[count++] = (uint8_t)'0';
  }
  while (value > 0) {
    digits[count++] = (uint8_t)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0 && pos + 1 < PAYLOAD_CAP) {
    reply_payload[pos++] = (char)digits[--count];
  }
  reply_payload[pos] = '\0';
  return pos;
}

/* answer_request composes the bounded bridge answer. The fixed method
 * allowlist is enforced here by the backend, not by the page. */
static uint32_t answer_request(const char *method, const char *payload) {
  uint32_t pos = 0;
  reply_payload[0] = '\0';
  if (text_equal(method, "greet")) {
    pos = r_text(pos, "Hello, ");
    if (payload[0] == '\0') {
      pos = r_text(pos, "from the plugin backend");
    } else {
      pos = r_text(pos, payload);
    }
    pos = r_text(pos, "! Served by acme.webview-view.\n");
    return STATUS_OK;
  }
  if (text_equal(method, "report")) {
    pos = r_text(pos, "webview sample: origin=natcatty-plugin://acme.webview-view");
    pos = r_text(pos, " tree=");
    pos = r_text(pos, apply_status == (int32_t)STATUS_OK ? "ready" : "denied");
    pos = r_text(pos, " revision=");
    pos = r_u32(pos, revision);
    pos = r_text(pos, " handled=");
    pos = r_u32(pos, handled_requests);
    pos = r_text(pos, " denied=");
    pos = r_u32(pos, denied_requests);
    pos = r_text(pos, "\n");
    return STATUS_OK;
  }
  pos = r_text(pos, "method is not in this View's bridge allowlist");
  return STATUS_DENIED;
}

static void send_reply(uint64_t handle, uint64_t request_id, uint32_t status) {
  uint32_t pos = 0;
  pos = put_u64(request, pos, handle);
  pos = put_u64(request, pos, request_id);
  pos = put_u32(request, pos, status);
  pos = put_str(request, pos, reply_payload);
  (void)call_ui(UI_WEBVIEW_RESPOND, request, pos);
}

/* --- poll -------------------------------------------------------------- */

/* poll_frames drains the queue and answers every webview request. A webview
 * event is session-wide: Node is 0, Related (offset 64) is the handle of the
 * page that sent it, and Text (offset 76) is the JSON envelope. */
static void poll_frames(void) {
  uint32_t pos = 0;
  uint32_t message_length;
  uint32_t count;
  uint32_t cursor;
  uint32_t index;
  int32_t transport;

  pos = put_u32(request, 0, 0); /* drain everything queued */
  transport = call_ui(UI_POLL, request, pos);
  if (transport != 0 || read_u32(response) != STATUS_OK) {
    return;
  }
  message_length = read_u32(response + 4);
  cursor = 8 + message_length;
  count = read_u32(response + cursor);
  cursor += 4;

  for (index = 0; index < count; index++) {
    uint32_t kind;
    uint32_t text_length;
    const uint8_t *text;
    char envelope_type[METHOD_CAP];
    char method[METHOD_CAP];
    char payload[PAYLOAD_CAP];
    uint64_t request_id = 0;
    uint64_t handle;
    uint32_t status;

    if (cursor + EVENT_FRAME > RESPONSE_CAP) {
      break;
    }
    kind = read_u32(response + cursor);
    text_length = read_u32(response + cursor + 72);
    if (cursor + EVENT_FRAME + text_length > RESPONSE_CAP) {
      break;
    }
    text = response + cursor + EVENT_FRAME;
    if (kind == EVENT_WEBVIEW) {
      handle = read_u64(response + cursor + 64);
      envelope_type[0] = '\0';
      method[0] = '\0';
      payload[0] = '\0';
      if (string_field(text, text_length, "\"type\":", envelope_type,
                       sizeof(envelope_type)) &&
          text_equal(envelope_type, "request") &&
          string_field(text, text_length, "\"method\":", method, sizeof(method)) &&
          u64_field(text, text_length, "\"request\":", &request_id)) {
        (void)string_field(text, text_length, "\"payload\":", payload,
                           sizeof(payload));
        text_copy(last_method, METHOD_CAP, method);
        status = answer_request(method, payload);
        if (status == STATUS_OK) {
          handled_requests++;
        } else {
          denied_requests++;
        }
        send_reply(handle, request_id, status);
      }
    }
    cursor += EVENT_FRAME + text_length;
  }
}

/* --- report ------------------------------------------------------------ */

static uint32_t report_pos;

static uint32_t o_text(const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && report_pos + 1 < REPORT_CAP) {
    report[report_pos++] = (uint8_t)text[i++];
  }
  return report_pos;
}

static uint32_t o_u32(uint32_t value) {
  uint8_t digits[10];
  uint32_t count = 0;
  if (value == 0) {
    digits[count++] = (uint8_t)'0';
  }
  while (value > 0) {
    digits[count++] = (uint8_t)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0 && report_pos + 1 < REPORT_CAP) {
    report[report_pos++] = digits[--count];
  }
  return report_pos;
}

static int32_t run_report(void) {
  report_pos = 0;
  o_text("webview sample\nui.apply status=");
  if (apply_status == (int32_t)STATUS_OK) {
    o_text("ok");
  } else if (apply_status == (int32_t)STATUS_DENIED) {
    o_text("denied");
  } else {
    o_text("other");
  }
  o_text(" revision=");
  o_u32(revision);
  o_text(" ui.listen status=");
  if (listen_status == (int32_t)STATUS_OK) {
    o_text("ok");
  } else if (listen_status == (int32_t)STATUS_DENIED) {
    o_text("denied");
  } else {
    o_text("other");
  }
  o_text(" handles.root=");
  o_u32(handle_root != 0 ? 1u : 0u);
  o_text(" handles.label=");
  o_u32(handle_label != 0 ? 1u : 0u);
  o_text(" handles.webview=");
  o_u32(handle_webview != 0 ? 1u : 0u);
  o_text(" handled=");
  o_u32(handled_requests);
  o_text(" denied=");
  o_u32(denied_requests);
  o_text(" last=");
  o_text(last_method);
  o_text("\nreport.done\n");
  return host_output_write(report, (int32_t)report_pos) < 0 ? -1 : 0;
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  apply_initial();
  listen_webview();
  return 0;
}

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  uint32_t ptr = (uint32_t)heap + heap_used;
  heap_used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr,
                                                       uint32_t command_len) {
  (void)command_ptr;
  (void)command_len;
  return run_report();
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) {
  poll_frames();
  return 0;
}

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
