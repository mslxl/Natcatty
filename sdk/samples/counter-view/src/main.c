/*
 * Counter View sample guest.
 *
 * This is a real WASM plugin built against the v1 native UI ABI. It keeps a
 * retained tree with a text line, an input, and a button:
 *
 *   - init         applies the first batch and registers listeners for the
 *                  input's input event and the button's click event
 *   - poll         drains the host queue through ui.poll, then commits one
 *                  atomic set_text patch per changed line
 *   - dispatch     writes a bounded report through output_write
 *
 * Every node and listener crosses the boundary as a generation-tagged handle
 * returned by ui.apply and ui.listen; the guest never stores host pointers.
 * With the optional ui.native grant the calls return status=ok; without it
 * every call returns status=denied and the host tree stays empty. Both paths
 * are exercised by e2e_test.go through the real Manager, consent, container,
 * and wazero runtime.
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

#define NODE_CONTAINER 0u
#define NODE_TEXT 1u
#define NODE_BUTTON 2u
#define NODE_INPUT 3u

#define OP_CREATE 0u
#define OP_SET_PROP 4u
#define OP_SET_TEXT 5u
#define OP_SET_CHILDREN 6u

#define PROP_LAYOUT 1u
#define PROP_LABEL 18u
#define PROP_ROLE 19u
#define PROP_PLACEHOLDER 23u

#define VALUE_INT 1u
#define VALUE_STRING 3u

#define EVENT_CLICK 0u
#define EVENT_INPUT 4u

#define ROLE_BUTTON 1u
#define LAYOUT_COLUMN 1

/* Native UI wire status values. */
#define STATUS_OK 0u
#define STATUS_DENIED 3u
/* Not a wire value: distinguishes a call that never ran from a wire status. */
#define STATUS_NOT_RUN 99

#define REQUEST_CAP 4096u
#define RESPONSE_CAP 32768u
#define REPORT_CAP 2048u
#define NAME_CAP 128u
#define PATCH_CAP 512u

static uint8_t heap[32768];
static uint32_t heap_used;
static uint8_t request[REQUEST_CAP];
static uint8_t response[RESPONSE_CAP];
static uint8_t patch[PATCH_CAP];
static uint8_t report[REPORT_CAP];
static char name[NAME_CAP];

/* Handles the host issued for this guest's nodes. Zero means not issued. */
static uint64_t handle_count_line;
static uint64_t handle_name_line;
static uint64_t handle_input;
static uint64_t handle_button;

static uint32_t revision;
static int32_t apply_status = STATUS_NOT_RUN;
static int32_t listen_status = STATUS_NOT_RUN;
static uint32_t counter;
static int rendered;

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

/* --- small text helpers ------------------------------------------------ */

static void text_copy(char *dst, uint32_t cap, const char *src) {
  uint32_t i = 0;
  while (src[i] != '\0' && i + 1 < cap) {
    dst[i] = src[i];
    i++;
  }
  dst[i] = '\0';
}

static int command_contains(uint32_t command_ptr, uint32_t command_len, const char *needle) {
  const uint8_t *command = (const uint8_t *)command_ptr;
  uint32_t length = (uint32_t)strlen(needle);
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

/* r_* append to the report buffer, leaving room for a trailing NUL. */
static uint32_t r_text(uint32_t pos, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < REPORT_CAP) {
    report[pos++] = (uint8_t)text[i++];
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

static const char *status_name(int32_t status) {
  if (status == STATUS_NOT_RUN) {
    return "not-run";
  }
  if (status == 0) {
    return "ok";
  }
  if (status == 1) {
    return "invalid";
  }
  if (status == 2) {
    return "limit";
  }
  if (status == 3) {
    return "denied";
  }
  if (status == 4) {
    return "stale-handle";
  }
  if (status == 5) {
    return "unavailable";
  }
  if (status == 6) {
    return "closed";
  }
  return "transport";
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

static void format_count_line(char *out, uint32_t cap) {
  static const char prefix[] = "Count: ";
  uint32_t pos = 0;
  uint8_t digits[10];
  uint32_t count = 0;
  uint32_t value = counter;
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) {
    out[pos++] = prefix[i++];
  }
  if (value == 0) {
    digits[count++] = (uint8_t)'0';
  }
  while (value > 0) {
    digits[count++] = (uint8_t)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0 && pos + 1 < cap) {
    out[pos++] = (char)digits[--count];
  }
  out[pos] = '\0';
}

static void format_name_line(char *out, uint32_t cap) {
  static const char prefix[] = "Hello, ";
  uint32_t pos = 0;
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) {
    out[pos++] = prefix[i++];
  }
  i = 0;
  while (name[i] != '\0' && pos + 1 < cap) {
    out[pos++] = name[i++];
  }
  if (pos + 1 < cap) {
    out[pos++] = '!';
  }
  out[pos] = '\0';
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
    if (id == 2) {
      handle_count_line = handle;
    } else if (id == 3) {
      handle_input = handle;
    } else if (id == 4) {
      handle_button = handle;
    } else if (id == 5) {
      handle_name_line = handle;
    }
  }
}

/* apply_initial builds the retained tree and asks the host to commit it. */
static void apply_initial(void) {
  static const uint32_t children[] = {2, 3, 4, 5};
  char count_line[32];
  uint32_t pos = 0;
  int32_t transport;

  if (rendered) {
    return;
  }
  rendered = 1;
  batch_buffer = request;
  format_count_line(count_line, sizeof(count_line));
  pos = put_u64(request, pos, 1); /* root */
  pos = put_u32(request, pos, 13);
  pos = add_create(pos, 1, NODE_CONTAINER);
  pos = add_create(pos, 2, NODE_TEXT);
  pos = add_create(pos, 3, NODE_INPUT);
  pos = add_create(pos, 4, NODE_BUTTON);
  pos = add_create(pos, 5, NODE_TEXT);
  pos = add_children(pos, 1, children, 4);
  pos = add_int_prop(pos, 1, PROP_LAYOUT, LAYOUT_COLUMN);
  pos = add_text(pos, 2, count_line);
  pos = add_string_prop(pos, 3, PROP_LABEL, "Name");
  pos = add_string_prop(pos, 3, PROP_PLACEHOLDER, "Your name");
  pos = add_string_prop(pos, 4, PROP_LABEL, "Increment");
  pos = add_int_prop(pos, 4, PROP_ROLE, ROLE_BUTTON);
  pos = add_text(pos, 5, "Hello, stranger!");
  transport = call_ui(UI_APPLY, request, pos);
  apply_status = response_status(transport);
  if (apply_status != (int32_t)STATUS_OK) {
    return;
  }
  revision = read_u32(response + 4);
  parse_created();

  batch_buffer = request;
  pos = 0;
  pos = put_u64(request, pos, handle_input);
  pos = put_u32(request, pos, EVENT_INPUT);
  transport = call_ui(UI_LISTEN, request, pos);
  listen_status = response_status(transport);
  if (listen_status == 0) {
    pos = 0;
    pos = put_u64(request, pos, handle_button);
    pos = put_u32(request, pos, EVENT_CLICK);
    transport = call_ui(UI_LISTEN, request, pos);
    listen_status = response_status(transport);
  }
}

/* handle_poll drains queued events and commits the patches they produce. */
static void handle_poll(void) {
  char line[160];
  uint32_t pos = 0;
  uint32_t message_length;
  uint32_t count;
  uint32_t cursor;
  uint32_t index;
  uint32_t ops = 0;
  uint32_t patch_pos;
  int count_changed = 0;
  int name_changed = 0;
  int32_t transport;

  if (apply_status != (int32_t)STATUS_OK) {
    return;
  }
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
    uint64_t node;
    uint32_t text_length;
    const uint8_t *text;
    if (cursor + 76 > RESPONSE_CAP) {
      break;
    }
    kind = read_u32(response + cursor);
    node = read_u64(response + cursor + 8);
    text_length = read_u32(response + cursor + 72);
    if (cursor + 76 + text_length > RESPONSE_CAP) {
      break;
    }
    text = response + cursor + 76;
    if (kind == EVENT_INPUT && node == handle_input) {
      uint32_t i;
      uint32_t copy = text_length;
      if (copy >= NAME_CAP) {
        copy = NAME_CAP - 1;
      }
      for (i = 0; i < copy; i++) {
        name[i] = (char)text[i];
      }
      name[copy] = '\0';
      name_changed = 1;
    } else if (kind == EVENT_CLICK && node == handle_button) {
      counter++;
      count_changed = 1;
    }
    cursor += 76 + text_length;
  }

  if (!count_changed && !name_changed) {
    return;
  }

  batch_buffer = patch;
  patch_pos = put_u64(patch, 0, 0); /* keep the current root */
  patch_pos = put_u32(patch, patch_pos, 0);
  if (count_changed) {
    format_count_line(line, sizeof(line));
    patch_pos = add_text(patch_pos, handle_count_line, line);
    ops++;
  }
  if (name_changed) {
    format_name_line(line, sizeof(line));
    patch_pos = add_text(patch_pos, handle_name_line, line);
    ops++;
  }
  /* The op count sits between the root and the first op. */
  patch[8] = (uint8_t)ops;
  patch[9] = (uint8_t)(ops >> 8);
  patch[10] = (uint8_t)(ops >> 16);
  patch[11] = (uint8_t)(ops >> 24);

  transport = call_ui(UI_APPLY, patch, patch_pos);
  if (transport == 0 && read_u32(response) == STATUS_OK) {
    revision = read_u32(response + 4);
  }
}

/* run_report writes the bounded state report the e2e test reads. */
static int32_t run_report(void) {
  uint32_t pos = 0;
  pos = r_text(pos, "counter sample\n");
  pos = r_text(pos, "ui.apply status=");
  pos = r_text(pos, status_name(apply_status));
  pos = r_text(pos, " revision=");
  pos = r_u32(pos, revision);
  pos = r_text(pos, "\nui.listen status=");
  pos = r_text(pos, status_name(listen_status));
  pos = r_text(pos, "\ncounter=");
  pos = r_u32(pos, counter);
  pos = r_text(pos, "\nname=");
  pos = r_text(pos, name);
  pos = r_text(pos, "\nhandles.count=");
  pos = r_u32(pos, handle_count_line != 0 ? 1u : 0u);
  pos = r_text(pos, " handles.input=");
  pos = r_u32(pos, handle_input != 0 ? 1u : 0u);
  pos = r_text(pos, " handles.button=");
  pos = r_u32(pos, handle_button != 0 ? 1u : 0u);
  pos = r_text(pos, "\nreport.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  apply_initial();
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
  handle_poll();
  return 0;
}

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
