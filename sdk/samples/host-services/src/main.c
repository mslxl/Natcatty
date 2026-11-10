/*
 * Host-services sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It imports every
 * generated natcatty service module and drives the baseline host services:
 *
 *   storage       set, get, list, delete
 *   clipboard     write, read
 *   notifications post (actionable and plain), dismiss
 *   events        subscribe to notification.action and poll
 *   settings      read a granted key and a key outside the grant
 *
 * The `selftest` command runs the suite and writes one bounded report through
 * output_write. The same module is useful in two permission modes: with the
 * optional grants the report carries values and notification IDs; without them
 * every call reports `code=denied permission=<service>` instead of silently
 * succeeding. The `poll` command drains the event subscription and prints the
 * host's response, including the events.gap the host emits on overflow.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
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

IMPORT("natcatty:svc/storage_v1", "call") extern int32_t storage_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/clipboard_v1", "call") extern int32_t clipboard_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/notifications_v1", "call") extern int32_t notifications_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/events_v1", "call") extern int32_t events_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/settings_v1", "call") extern int32_t settings_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

/* Stable method ids from the generated IDL. */
#define STORAGE_GET 1u
#define STORAGE_SET 2u
#define STORAGE_DELETE 3u
#define STORAGE_LIST 4u
#define CLIPBOARD_READ 1u
#define CLIPBOARD_WRITE 2u
#define NOTIFICATIONS_POST 1u
#define NOTIFICATIONS_DISMISS 2u
#define EVENTS_SUBSCRIBE 1u
#define EVENTS_POLL 2u
#define SETTINGS_READ 1u

#define SVC_STORAGE 0u
#define SVC_CLIPBOARD 1u
#define SVC_NOTIFICATIONS 2u
#define SVC_EVENTS 3u
#define SVC_SETTINGS 4u

#define RESPONSE_CAP 4096u
#define REPORT_CAP 16384u

static uint8_t heap[65536];
static uint32_t heap_used;
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];

/* Handle returned by the notification.action subscription, as decimal text. */
static char action_handle[32];
/* Notification ID used by the dismiss call, as JSON-safe text. */
static char dismiss_id[32];

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

static int bytes_start_with(const uint8_t *data, const char *prefix, uint32_t length) {
  uint32_t i;
  for (i = 0; i < length; i++) {
    if (data[i] != (uint8_t)prefix[i]) {
      return 0;
    }
  }
  return 1;
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
    if (bytes_start_with(command + i, needle, length)) {
      return 1;
    }
  }
  return 0;
}

/* r_* append to the report buffer, leaving room for a trailing byte. */
static uint32_t r_text(uint32_t pos, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < REPORT_CAP) {
    report[pos++] = (uint8_t)text[i++];
  }
  return pos;
}

static uint32_t r_bytes(uint32_t pos, const uint8_t *data, uint32_t length) {
  uint32_t i;
  for (i = 0; i < length && pos + 1 < REPORT_CAP; i++) {
    report[pos++] = data[i];
  }
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
  while (count > 0 && pos + 1 < REPORT_CAP) {
    report[pos++] = digits[--count];
  }
  return pos;
}

static uint32_t r_i32(uint32_t pos, int32_t value) {
  if (value < 0) {
    if (pos + 1 < REPORT_CAP) {
      report[pos++] = (uint8_t)'-';
    }
    return r_u32(pos, (uint32_t)(-value));
  }
  return r_u32(pos, (uint32_t)value);
}

static int32_t call_service(uint32_t service, uint32_t method, const char *request,
                            uint32_t request_len) {
  uint32_t i;
  for (i = 0; i < RESPONSE_CAP; i++) {
    response[i] = 0;
  }
  switch (service) {
    case SVC_STORAGE:
      return storage_call(method, (const uint8_t *)request, request_len,
                          response, RESPONSE_CAP - 1u);
    case SVC_CLIPBOARD:
      return clipboard_call(method, (const uint8_t *)request, request_len,
                            response, RESPONSE_CAP - 1u);
    case SVC_NOTIFICATIONS:
      return notifications_call(method, (const uint8_t *)request, request_len,
                                response, RESPONSE_CAP - 1u);
    case SVC_EVENTS:
      return events_call(method, (const uint8_t *)request, request_len,
                         response, RESPONSE_CAP - 1u);
    default:
      return settings_call(method, (const uint8_t *)request, request_len,
                           response, RESPONSE_CAP - 1u);
  }
}

/* field_value copies the string value of "field":"value" from the response
 * envelope. The values this sample reads contain no JSON escapes. */
static int field_value(const char *field, char *out, uint32_t cap) {
  uint32_t name_len = text_len(field);
  uint32_t i = 0;
  while (response[i] != 0) {
    if (response[i] == '"' && bytes_start_with(response + i + 1, field, name_len) &&
        response[i + 1 + name_len] == '"' && response[i + 2 + name_len] == ':' &&
        response[i + 3 + name_len] == '"') {
      uint32_t j = i + 4 + name_len;
      uint32_t k = 0;
      while (response[j] != 0 && response[j] != '"' && k + 1 < cap) {
        out[k++] = (char)response[j++];
      }
      out[k] = '\0';
      return 1;
    }
    i++;
  }
  return 0;
}

static int response_contains(const char *needle) {
  uint32_t length = text_len(needle);
  uint32_t i = 0;
  if (length == 0) {
    return 1;
  }
  while (response[i] != 0) {
    if (bytes_start_with(response + i, needle, length)) {
      return 1;
    }
    i++;
  }
  return 0;
}

static uint32_t emit_result(uint32_t pos, const char *label, int32_t status,
                            const char *value_field) {
  char value[256];
  pos = r_text(pos, label);
  pos = r_text(pos, " code=");
  if (status != 0) {
    pos = r_text(pos, "transport:");
    pos = r_i32(pos, status);
    pos = r_text(pos, "\n");
    return pos;
  }
  if (field_value("code", value, sizeof(value))) {
    pos = r_text(pos, value);
  } else {
    pos = r_text(pos, "unknown");
  }
  if (field_value("permission", value, sizeof(value))) {
    pos = r_text(pos, " permission=");
    pos = r_text(pos, value);
  }
  if (value_field != 0 && field_value(value_field, value, sizeof(value))) {
    pos = r_text(pos, " ");
    pos = r_text(pos, value_field);
    pos = r_text(pos, "=");
    pos = r_text(pos, value);
  }
  pos = r_text(pos, "\n");
  return pos;
}

static uint32_t build_id_request(char *buffer, uint32_t cap, const char *id) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"id\":\"";
  static const char suffix[] = "\"}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = prefix[i++];
  }
  i = 0;
  while (id[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = id[i++];
  }
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = suffix[i++];
  }
  buffer[pos] = '\0';
  return pos;
}

static uint32_t build_poll_request(char *buffer, uint32_t cap, const char *handle,
                                   uint32_t limit) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"handle\":\"";
  static const char middle[] = "\",\"limit\":";
  static const char suffix[] = "}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = prefix[i++];
  }
  i = 0;
  while (handle[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = handle[i++];
  }
  i = 0;
  while (middle[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = middle[i++];
  }
  {
    uint8_t digits[10];
    uint32_t count = 0;
    if (limit == 0) {
      digits[count++] = (uint8_t)'0';
    }
    while (limit > 0) {
      digits[count++] = (uint8_t)('0' + (limit % 10u));
      limit /= 10u;
    }
    while (count > 0 && pos + 1 < cap) {
      buffer[pos++] = (char)digits[--count];
    }
  }
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = suffix[i++];
  }
  buffer[pos] = '\0';
  return pos;
}

static uint32_t build_post_label(char *buffer, uint32_t cap, uint32_t index) {
  uint32_t pos = 0;
  static const char prefix[] = "notifications.post.";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) {
    buffer[pos++] = prefix[i++];
  }
  if (pos + 2 < cap) {
    buffer[pos++] = (char)('0' + (index / 10u));
    buffer[pos++] = (char)('0' + (index % 10u));
  }
  buffer[pos] = '\0';
  return pos;
}

static int32_t run_selftest(void) {
  static const char storage_set_request[] =
      "{\"key\":\"sample-greeting\",\"value\":\"aGVsbG8=\"}";
  static const char storage_get_request[] = "{\"key\":\"sample-greeting\"}";
  static const char storage_list_request[] = "{\"prefix\":\"sample-\"}";
  static const char storage_delete_request[] = "{\"key\":\"sample-greeting\"}";
  static const char clipboard_write_request[] = "{\"text\":\"host services sample\"}";
  static const char clipboard_read_request[] = "{}";
  static const char subscribe_actions_request[] = "{\"type\":\"notification.action\"}";
  static const char subscribe_terminal_request[] = "{\"type\":\"terminal.exit\"}";
  static const char settings_locale_request[] = "{\"key\":\"app.locale\"}";
  static const char settings_version_request[] = "{\"key\":\"app.version\"}";
  static const char *action_requests[4] = {
      "{\"title\":\"Host services 1\",\"body\":\"Choose an action\","
      "\"actions\":[{\"id\":\"open\",\"label\":\"Open\"}]}",
      "{\"title\":\"Host services 2\",\"body\":\"Choose an action\","
      "\"actions\":[{\"id\":\"open\",\"label\":\"Open\"}]}",
      "{\"title\":\"Host services 3\",\"body\":\"Choose an action\","
      "\"actions\":[{\"id\":\"open\",\"label\":\"Open\"}]}",
      "{\"title\":\"Host services 4\",\"body\":\"Choose an action\","
      "\"actions\":[{\"id\":\"open\",\"label\":\"Open\"}]}",
  };
  static const char dismiss_post_request[] =
      "{\"title\":\"Host services dismiss\",\"body\":\"Dismiss me\"}";
  static const uint8_t log_line[] = "host-services sample log line";
  char value[256];
  char label[32];
  char request[128];
  uint32_t pos = 0;
  uint32_t i;
  int32_t status;

  status = call_service(SVC_STORAGE, STORAGE_SET, storage_set_request,
                        sizeof(storage_set_request) - 1);
  pos = emit_result(pos, "storage.set", status, 0);

  status = call_service(SVC_STORAGE, STORAGE_GET, storage_get_request,
                        sizeof(storage_get_request) - 1);
  pos = emit_result(pos, "storage.get", status, "value");
  if (status == 0 && field_value("value", value, sizeof(value)) &&
      text_eq(value, "aGVsbG8=")) {
    pos = r_text(pos, "storage.get.match=ok\n");
  } else {
    pos = r_text(pos, "storage.get.match=no\n");
  }

  status = call_service(SVC_STORAGE, STORAGE_LIST, storage_list_request,
                        sizeof(storage_list_request) - 1);
  pos = emit_result(pos, "storage.list", status, 0);
  if (status == 0 && response_contains("sample-greeting")) {
    pos = r_text(pos, "storage.list.match=ok\n");
  } else {
    pos = r_text(pos, "storage.list.match=no\n");
  }

  status = call_service(SVC_STORAGE, STORAGE_DELETE, storage_delete_request,
                        sizeof(storage_delete_request) - 1);
  pos = emit_result(pos, "storage.delete", status, 0);

  status = call_service(SVC_CLIPBOARD, CLIPBOARD_WRITE, clipboard_write_request,
                        sizeof(clipboard_write_request) - 1);
  pos = emit_result(pos, "clipboard.write", status, 0);

  status = call_service(SVC_CLIPBOARD, CLIPBOARD_READ, clipboard_read_request,
                        sizeof(clipboard_read_request) - 1);
  pos = emit_result(pos, "clipboard.read", status, "text");
  if (status == 0 && field_value("text", value, sizeof(value)) &&
      text_eq(value, "host services sample")) {
    pos = r_text(pos, "clipboard.read.match=ok\n");
  } else {
    pos = r_text(pos, "clipboard.read.match=no\n");
  }

  action_handle[0] = '\0';
  status = call_service(SVC_EVENTS, EVENTS_SUBSCRIBE, subscribe_actions_request,
                        sizeof(subscribe_actions_request) - 1);
  pos = emit_result(pos, "events.subscribe.actions", status, "handle");
  if (status == 0 && field_value("handle", value, sizeof(value))) {
    text_copy(action_handle, sizeof(action_handle), value);
  }

  for (i = 0; i < 4; i++) {
    build_post_label(label, sizeof(label), i + 1);
    status = call_service(SVC_NOTIFICATIONS, NOTIFICATIONS_POST, action_requests[i],
                          text_len(action_requests[i]));
    pos = emit_result(pos, label, status, "id");
  }

  dismiss_id[0] = '\0';
  status = call_service(SVC_NOTIFICATIONS, NOTIFICATIONS_POST, dismiss_post_request,
                        sizeof(dismiss_post_request) - 1);
  pos = emit_result(pos, "notifications.post.dismiss", status, "id");
  if (status == 0 && field_value("id", value, sizeof(value))) {
    text_copy(dismiss_id, sizeof(dismiss_id), value);
    build_id_request(request, sizeof(request), dismiss_id);
    status = call_service(SVC_NOTIFICATIONS, NOTIFICATIONS_DISMISS, request,
                          text_len(request));
    pos = emit_result(pos, "notifications.dismiss", status, 0);
  } else {
    pos = r_text(pos, "notifications.dismiss skipped\n");
  }

  status = call_service(SVC_SETTINGS, SETTINGS_READ, settings_locale_request,
                        sizeof(settings_locale_request) - 1);
  pos = emit_result(pos, "settings.read.locale", status, "value");

  status = call_service(SVC_SETTINGS, SETTINGS_READ, settings_version_request,
                        sizeof(settings_version_request) - 1);
  pos = emit_result(pos, "settings.read.version", status, "value");

  status = call_service(SVC_EVENTS, EVENTS_SUBSCRIBE, subscribe_terminal_request,
                        sizeof(subscribe_terminal_request) - 1);
  pos = emit_result(pos, "events.subscribe.terminal", status, "handle");

  /* Bounded host logging: the host's rate limiter and ring cap this burst. */
  for (i = 0; i < 500; i++) {
    host_log(2, log_line, (int32_t)(sizeof(log_line) - 1));
  }
  pos = r_text(pos, "logging calls=500\n");

  pos = r_text(pos, "selftest.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

static int32_t run_poll(void) {
  static const char unsubscribed[] = "poll code=unsubscribed\n";
  char request[128];
  char value[256];
  uint32_t pos = 0;
  uint32_t length;
  int32_t status;

  if (action_handle[0] == '\0') {
    pos = r_text(pos, unsubscribed);
  } else {
    length = build_poll_request(request, sizeof(request), action_handle, 64);
    status = call_service(SVC_EVENTS, EVENTS_POLL, request, length);
    pos = r_text(pos, "poll code=");
    if (status != 0) {
      pos = r_text(pos, "transport:");
      pos = r_i32(pos, status);
    } else if (field_value("code", value, sizeof(value))) {
      pos = r_text(pos, value);
    } else {
      pos = r_text(pos, "unknown");
    }
    pos = r_text(pos, "\n");
    if (status == 0) {
      pos = r_text(pos, "poll.response=");
      pos = r_bytes(pos, response, text_len((const char *)response));
      pos = r_text(pos, "\n");
    }
  }
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
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
  if (command_contains(command_ptr, command_len, "poll")) {
    return run_poll();
  }
  return run_selftest();
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
