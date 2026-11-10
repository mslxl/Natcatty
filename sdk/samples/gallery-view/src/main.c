/*
 * Gallery View sample guest.
 *
 * This is a real WASM plugin built against the v1 native UI ABI (1.9.0). It
 * exercises every data-oriented and rich-content native widget:
 *
 *   - a data grid declaring 50,000 rows while patching only the sparse
 *     window the host asks for through item_request
 *   - a graph with explicit layout, an edge, selection, and activation
 *   - a plan graph with statuses, costs, and annotations whose interaction
 *     stays attributed to the plan_graph node
 *   - a canvas drawing a rect, line, ellipse, text, and a packaged image,
 *     reporting pointer input in canvas-local coordinates
 *   - host-rendered rich text with styled spans, a literal-markup span, and
 *     an inline link, plus a huge virtual list that pulls items through
 *     item_request
 *   - a packaged SVG image and packaged TrueType font, resolved by the host
 *     from the plugin's own asset directory
 *   - a host-driven width transition on a container, reported back once as
 *     animation_end
 *
 * init builds the initial batch, registers listeners, and probes three
 * invalid batches (an out-of-kind property, a remote asset URL, and a stale
 * handle) to prove they are rejected atomically. poll drains events, appends
 * requested grid and list items in bounded patches, and records
 * interactions. dispatch writes a bounded report the e2e test reads. Every
 * node and listener crosses the boundary as a generation-tagged handle; the
 * guest never stores host pointers.
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
#define NODE_IMAGE 11u
#define NODE_DATA_GRID 20u
#define NODE_DATA_COLUMN 21u
#define NODE_DATA_ROW 22u
#define NODE_GRAPH 23u
#define NODE_GRAPH_NODE 24u
#define NODE_GRAPH_EDGE 25u
#define NODE_PLAN_GRAPH 26u
#define NODE_PLAN_NODE 27u
#define NODE_CANVAS 28u
#define NODE_CANVAS_SHAPE 29u
#define NODE_RICH_TEXT 30u
#define NODE_TEXT_SPAN 31u
#define NODE_VIRTUAL_LIST 32u
#define NODE_VIRTUAL_ITEM 33u

#define OP_CREATE 0u
#define OP_INSERT 2u
#define OP_SET_PROP 4u
#define OP_SET_TEXT 5u
#define OP_SET_CHILDREN 6u

#define PROP_LAYOUT 1u
#define PROP_GAP 2u
#define PROP_WIDTH 11u
#define PROP_HEIGHT 12u
#define PROP_LABEL 18u
#define PROP_BACKGROUND_TOKEN 31u
#define PROP_TEXT_TOKEN 32u
#define PROP_FONT_SIZE 35u
#define PROP_FONT_WEIGHT 36u
#define PROP_SRC 38u
#define PROP_ALT 39u
#define PROP_EXPANDED 41u
#define PROP_ROW_COUNT 44u
#define PROP_ROW_INDEX 45u
#define PROP_SORTABLE 46u
#define PROP_FROM 47u
#define PROP_TO 48u
#define PROP_DIRECTED 49u
#define PROP_X 50u
#define PROP_Y 51u
#define PROP_STROKE_TOKEN 52u
#define PROP_FILL_TOKEN 53u
#define PROP_LINE_WIDTH 54u
#define PROP_SHAPE 55u
#define PROP_ANNOTATION 56u
#define PROP_COST 57u
#define PROP_PLAN_STATUS 58u
#define PROP_LINK 59u
#define PROP_ITALIC 60u
#define PROP_UNDERLINE 61u
#define PROP_STRIKETHROUGH 62u
#define PROP_FONT_ASSET 63u
#define PROP_ITEM_COUNT 64u
#define PROP_ITEM_INDEX 65u
#define PROP_TRANSITION_MS 66u
#define PROP_TRANSITION_EASE 67u

#define VALUE_BOOL 0u
#define VALUE_INT 1u
#define VALUE_FLOAT 2u
#define VALUE_STRING 3u
#define VALUE_REF 4u

#define EVENT_CLICK 0u
#define EVENT_DOUBLE_CLICK 1u
#define EVENT_CHANGE 3u
#define EVENT_SUBMIT 5u
#define EVENT_SCROLL 10u
#define EVENT_SELECTION 11u
#define EVENT_DRAG 13u
#define EVENT_ITEM_REQUEST 16u
#define EVENT_ANIMATION_END 17u

#define LAYOUT_ROW 0
#define LAYOUT_COLUMN 1

#define CANVAS_RECT 0
#define CANVAS_LINE 1
#define CANVAS_ELLIPSE 2
#define CANVAS_TEXT 3
#define CANVAS_IMAGE 4

#define PLAN_RUNNING 1
#define PLAN_DONE 2
#define PLAN_FAILED 3

#define TOKEN_SURFACE 1
#define TOKEN_BORDER 4
#define TOKEN_TEXT 5
#define TOKEN_TEXT_MUTED 6
#define TOKEN_ACCENT 7
#define TOKEN_SUCCESS 11

/* Native UI wire status values. */
#define STATUS_OK 0u
/* Not a wire value: distinguishes a call that never ran from a wire status. */
#define STATUS_NOT_RUN 99

#define REQUEST_CAP 65536u
#define RESPONSE_CAP 65536u
#define REPORT_CAP 4096u
#define TEXT_CAP 64u

#define GRID_ROW_COUNT 50000
#define INITIAL_ROWS 6
#define MAX_ROWS_PER_REQUEST 16
#define ROW_NODE_OPS 9
#define ROW_INSERT_OPS 1

#define VIRTUAL_ITEM_COUNT 250000
#define INITIAL_VIRTUAL_ITEMS 3
#define MAX_VIRTUAL_ITEMS_PER_REQUEST 12
#define VIRTUAL_ITEM_OPS 3
#define VIRTUAL_INSERT_OPS 1

#define EASE_LINEAR 0
#define ANIMATE_NARROW 80
#define ANIMATE_WIDE 220
#define ANIMATE_DURATION_MS 120

static uint8_t heap[65536];
static uint32_t heap_used;
static uint8_t request[REQUEST_CAP];
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];

/* Handles the host issued for this guest's nodes. Zero means not issued. */
static uint64_t handle_heading;
static uint64_t handle_grid;
static uint64_t handle_graph;
static uint64_t handle_graph_load;
static uint64_t handle_graph_save;
static uint64_t handle_graph_transform;
static uint64_t handle_plan;
static uint64_t handle_plan_seq;
static uint64_t handle_plan_index;
static uint64_t handle_canvas;
static uint64_t handle_rich;
static uint64_t handle_virtual;
static uint64_t handle_image;
static uint64_t handle_animate;
static uint64_t handle_animate_target;

static uint32_t revision;
static int32_t apply_status = STATUS_NOT_RUN;
static int32_t listen_status = STATUS_NOT_RUN;
static uint32_t listener_count;
static int rendered;

/* Grid paging state. */
static uint32_t next_node_id = 1000;
static uint32_t grid_children = 3 + INITIAL_ROWS;
static uint32_t grid_rows = INITIAL_ROWS;
static uint32_t grid_requests;
static int32_t last_request_index = -1;

/* Virtual list paging state. */
static uint32_t virtual_children = INITIAL_VIRTUAL_ITEMS;
static uint32_t virtual_rows = INITIAL_VIRTUAL_ITEMS;
static uint32_t virtual_requests;
static int32_t virtual_last_request = -1;

/* Interaction counters the report exposes. */
static uint32_t grid_selections;
static uint32_t grid_sorts;
static int32_t grid_sort_index = -1;
static int32_t grid_sort_direction = -1;
static uint32_t graph_selections;
static int32_t graph_selection_index = -1;
static int32_t graph_selection_related = -1;
static uint32_t graph_activations;
static uint32_t plan_selections;
static int32_t plan_selection_index = -1;
static uint32_t plan_activations;
static uint32_t plan_escaped;
static uint32_t canvas_clicks;
static int32_t canvas_click_x;
static int32_t canvas_click_y;
static uint32_t canvas_drags;
static int32_t canvas_drag_x;
static int32_t canvas_drag_y;
static int32_t canvas_drag_dx;
static int32_t canvas_drag_dy;
static uint32_t scroll_events;
static int32_t scroll_dy;
static uint32_t poll_calls;

/* Rich content interaction the report exposes. */
static uint32_t rich_selections;
static uint32_t rich_selection_chars;
static char rich_selection_text[TEXT_CAP];
static uint32_t virtual_clicks;
static int32_t virtual_click_index = -1;
static uint32_t virtual_selections;
static int32_t virtual_selection_index = -1;
static uint32_t animation_changes;
static uint32_t animation_ends;
static int animate_wide;

/* Results of the three invalid-batch probes. */
static int32_t invalid_prop_status = STATUS_NOT_RUN;
static int32_t invalid_asset_status = STATUS_NOT_RUN;
static int32_t invalid_handle_status = STATUS_NOT_RUN;
static int revision_intact;

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

static float read_f32(const uint8_t *in) {
  union {
    uint32_t u;
    float f;
  } bits;
  bits.u = read_u32(in);
  return bits.f;
}

static uint64_t double_bits(double value) {
  union {
    double d;
    uint64_t u;
  } bits;
  bits.d = value;
  return bits.u;
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

static uint32_t append_text(char *out, uint32_t pos, uint32_t cap, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1 < cap) {
    out[pos++] = text[i++];
  }
  out[pos] = '\0';
  return pos;
}

static uint32_t append_u32(char *out, uint32_t pos, uint32_t cap, uint32_t value) {
  uint8_t digits[10];
  uint32_t count = 0;
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
  return pos;
}

static uint32_t append_i32(char *out, uint32_t pos, uint32_t cap, int32_t value) {
  if (value < 0) {
    if (pos + 1 < cap) {
      out[pos++] = '-';
    }
    out[pos] = '\0';
    return append_u32(out, pos, cap, (uint32_t)(-value));
  }
  return append_u32(out, pos, cap, (uint32_t)value);
}

static void copy_event_text(const uint8_t *data, uint32_t length) {
  uint32_t i;
  uint32_t limit = length;
  if (limit > TEXT_CAP - 1u) {
    limit = TEXT_CAP - 1u;
  }
  for (i = 0; i < limit; i++) {
    rich_selection_text[i] = (char)data[i];
  }
  rich_selection_text[limit] = '\0';
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

static uint32_t add_insert(uint32_t pos, uint64_t parent, uint32_t index, uint32_t child) {
  pos = put_u8(batch_buffer, pos, OP_INSERT);
  pos = put_u64(batch_buffer, pos, parent);
  pos = put_u32(batch_buffer, pos, index);
  return put_u64(batch_buffer, pos, (uint64_t)child);
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

static uint32_t add_bool_prop(uint32_t pos, uint64_t node, uint32_t prop, int value) {
  pos = put_u8(batch_buffer, pos, OP_SET_PROP);
  pos = put_u64(batch_buffer, pos, node);
  pos = put_u32(batch_buffer, pos, prop);
  pos = put_u32(batch_buffer, pos, VALUE_BOOL);
  pos = put_u64(batch_buffer, pos, value ? 1u : 0u);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, 0);
  return put_str(batch_buffer, pos, "");
}

static uint32_t add_float_prop(uint32_t pos, uint64_t node, uint32_t prop, double value) {
  pos = put_u8(batch_buffer, pos, OP_SET_PROP);
  pos = put_u64(batch_buffer, pos, node);
  pos = put_u32(batch_buffer, pos, prop);
  pos = put_u32(batch_buffer, pos, VALUE_FLOAT);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, double_bits(value));
  pos = put_u64(batch_buffer, pos, 0);
  return put_str(batch_buffer, pos, "");
}

static uint32_t add_ref_prop(uint32_t pos, uint64_t node, uint32_t prop, uint64_t ref) {
  pos = put_u8(batch_buffer, pos, OP_SET_PROP);
  pos = put_u64(batch_buffer, pos, node);
  pos = put_u32(batch_buffer, pos, prop);
  pos = put_u32(batch_buffer, pos, VALUE_REF);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, 0);
  pos = put_u64(batch_buffer, pos, ref);
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

/* --- row generation ----------------------------------------------------- */

static void format_row_name(char *out, uint32_t cap, int32_t index) {
  uint32_t pos = 0;
  pos = append_text(out, pos, cap, "asset_");
  pos = append_i32(out, pos, cap, index);
  append_text(out, pos, cap, ".bin");
}

static void format_row_size(char *out, uint32_t cap, int32_t index) {
  uint32_t pos = 0;
  pos = append_i32(out, pos, cap, 1024 + index * 16);
  append_text(out, pos, cap, " KB");
}

static void format_feed_row(char *out, uint32_t cap, int32_t index) {
  uint32_t pos = 0;
  pos = append_text(out, pos, cap, "feed row ");
  append_i32(out, pos, cap, index);
}

/* add_row appends the nine ops that create one grid row and its three text
 * cells. The cells are referenced by their guest ids, which stay valid for
 * the duration of this batch. */
static uint32_t add_row(uint32_t pos, uint32_t row_id, int32_t index) {
  char name[TEXT_CAP];
  char size[TEXT_CAP];
  uint32_t cells[3];
  format_row_name(name, sizeof(name), index);
  format_row_size(size, sizeof(size), index);
  cells[0] = row_id + 1;
  cells[1] = row_id + 2;
  cells[2] = row_id + 3;
  pos = add_create(pos, row_id, NODE_DATA_ROW);
  pos = add_create(pos, cells[0], NODE_TEXT);
  pos = add_create(pos, cells[1], NODE_TEXT);
  pos = add_create(pos, cells[2], NODE_TEXT);
  pos = add_children(pos, row_id, cells, 3);
  pos = add_int_prop(pos, row_id, PROP_ROW_INDEX, index);
  pos = add_text(pos, cells[0], name);
  pos = add_text(pos, cells[1], size);
  return add_text(pos, cells[2], (index % 5) == 0 ? "dir" : "file");
}

/* --- apply responses ---------------------------------------------------- */

static void remember_handle(uint32_t id, uint64_t handle) {
  if (id == 2) {
    handle_heading = handle;
  } else if (id == 10) {
    handle_grid = handle;
  } else if (id == 20) {
    handle_graph = handle;
  } else if (id == 21) {
    handle_graph_load = handle;
  } else if (id == 22) {
    handle_graph_save = handle;
  } else if (id == 23) {
    handle_graph_transform = handle;
  } else if (id == 30) {
    handle_plan = handle;
  } else if (id == 31) {
    handle_plan_seq = handle;
  } else if (id == 32) {
    handle_plan_index = handle;
  } else if (id == 40) {
    handle_canvas = handle;
  } else if (id == 50) {
    handle_rich = handle;
  } else if (id == 60) {
    handle_virtual = handle;
  } else if (id == 70) {
    handle_image = handle;
  } else if (id == 80) {
    handle_animate = handle;
  } else if (id == 81) {
    handle_animate_target = handle;
  }
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
    remember_handle(id, handle);
  }
}

/* finish_apply calls ui.apply with the request length and, on success,
 * records the revision and any handles the batch created. */
static int32_t finish_apply(uint32_t length) {
  int32_t transport = call_ui(UI_APPLY, request, length);
  int32_t status = response_status(transport);
  if (status == (int32_t)STATUS_OK) {
    parse_created();
  }
  return status;
}

static void set_op_count(uint32_t ops) {
  request[8] = (uint8_t)ops;
  request[9] = (uint8_t)(ops >> 8);
  request[10] = (uint8_t)(ops >> 16);
  request[11] = (uint8_t)(ops >> 24);
}

/* apply_initial builds the retained tree and asks the host to commit it.
 * Every node is created before the op that references it, so inserts, child
 * lists, and ref properties all resolve inside this one atomic batch. */
static void apply_initial(void) {
  static const uint32_t row_ids[INITIAL_ROWS] = {100, 104, 108, 112, 116, 120};
  uint32_t pos = 0;
  uint32_t ops = 0;
  uint32_t i;
  int32_t status;

  if (rendered) {
    return;
  }
  rendered = 1;
  batch_buffer = request;
#define OP_ADD(expr)                                                                                                   \
  do {                                                                                                                 \
    pos = (expr);                                                                                                      \
    ops++;                                                                                                             \
  } while (0)

  pos = put_u64(request, pos, 1); /* root */
  pos = put_u32(request, pos, 0); /* ops count patched at the end */

  OP_ADD(add_create(pos, 1, NODE_CONTAINER));
  OP_ADD(add_int_prop(pos, 1, PROP_LAYOUT, LAYOUT_COLUMN));
  OP_ADD(add_float_prop(pos, 1, PROP_GAP, 10));

  OP_ADD(add_create(pos, 2, NODE_TEXT));
  OP_ADD(add_text(pos, 2, "Widget gallery"));
  OP_ADD(add_insert(pos, 1, 0, 2));

  /* Data grid: 50,000 declared rows, six patched initially. */
  OP_ADD(add_create(pos, 10, NODE_DATA_GRID));
  OP_ADD(add_string_prop(pos, 10, PROP_LABEL, "Inventory"));
  OP_ADD(add_int_prop(pos, 10, PROP_ROW_COUNT, GRID_ROW_COUNT));
  OP_ADD(add_float_prop(pos, 10, PROP_WIDTH, 640));
  OP_ADD(add_float_prop(pos, 10, PROP_HEIGHT, 260));
  OP_ADD(add_insert(pos, 1, 1, 10));

  OP_ADD(add_create(pos, 11, NODE_DATA_COLUMN));
  OP_ADD(add_string_prop(pos, 11, PROP_LABEL, "Name"));
  OP_ADD(add_float_prop(pos, 11, PROP_WIDTH, 240));
  OP_ADD(add_bool_prop(pos, 11, PROP_SORTABLE, 1));
  OP_ADD(add_insert(pos, 10, 0, 11));
  OP_ADD(add_create(pos, 12, NODE_DATA_COLUMN));
  OP_ADD(add_string_prop(pos, 12, PROP_LABEL, "Size"));
  OP_ADD(add_float_prop(pos, 12, PROP_WIDTH, 120));
  OP_ADD(add_insert(pos, 10, 1, 12));
  OP_ADD(add_create(pos, 13, NODE_DATA_COLUMN));
  OP_ADD(add_string_prop(pos, 13, PROP_LABEL, "Kind"));
  OP_ADD(add_float_prop(pos, 13, PROP_WIDTH, 100));
  OP_ADD(add_insert(pos, 10, 2, 13));

  for (i = 0; i < INITIAL_ROWS; i++) {
    pos = add_row(pos, row_ids[i], (int32_t)i);
    ops += ROW_NODE_OPS;
    OP_ADD(add_insert(pos, 10, 3 + i, row_ids[i]));
  }

  /* Graph with explicit coordinates and two directed edges. */
  OP_ADD(add_create(pos, 20, NODE_GRAPH));
  OP_ADD(add_string_prop(pos, 20, PROP_LABEL, "Build graph"));
  OP_ADD(add_float_prop(pos, 20, PROP_WIDTH, 640));
  OP_ADD(add_float_prop(pos, 20, PROP_HEIGHT, 180));
  OP_ADD(add_int_prop(pos, 20, PROP_LAYOUT, LAYOUT_ROW));
  OP_ADD(add_insert(pos, 1, 2, 20));

  OP_ADD(add_create(pos, 21, NODE_GRAPH_NODE));
  OP_ADD(add_text(pos, 21, "Load"));
  OP_ADD(add_float_prop(pos, 21, PROP_X, 20));
  OP_ADD(add_float_prop(pos, 21, PROP_Y, 20));
  OP_ADD(add_float_prop(pos, 21, PROP_WIDTH, 130));
  OP_ADD(add_float_prop(pos, 21, PROP_HEIGHT, 44));
  OP_ADD(add_insert(pos, 20, 0, 21));
  OP_ADD(add_create(pos, 22, NODE_GRAPH_NODE));
  OP_ADD(add_text(pos, 22, "Save"));
  OP_ADD(add_float_prop(pos, 22, PROP_X, 240));
  OP_ADD(add_float_prop(pos, 22, PROP_Y, 20));
  OP_ADD(add_float_prop(pos, 22, PROP_WIDTH, 130));
  OP_ADD(add_float_prop(pos, 22, PROP_HEIGHT, 44));
  OP_ADD(add_insert(pos, 20, 1, 22));
  OP_ADD(add_create(pos, 23, NODE_GRAPH_NODE));
  OP_ADD(add_text(pos, 23, "Transform"));
  OP_ADD(add_float_prop(pos, 23, PROP_X, 130));
  OP_ADD(add_float_prop(pos, 23, PROP_Y, 110));
  OP_ADD(add_float_prop(pos, 23, PROP_WIDTH, 150));
  OP_ADD(add_float_prop(pos, 23, PROP_HEIGHT, 44));
  OP_ADD(add_insert(pos, 20, 2, 23));

  OP_ADD(add_create(pos, 24, NODE_GRAPH_EDGE));
  OP_ADD(add_ref_prop(pos, 24, PROP_FROM, 21));
  OP_ADD(add_ref_prop(pos, 24, PROP_TO, 22));
  OP_ADD(add_bool_prop(pos, 24, PROP_DIRECTED, 1));
  OP_ADD(add_float_prop(pos, 24, PROP_LINE_WIDTH, 2));
  OP_ADD(add_int_prop(pos, 24, PROP_STROKE_TOKEN, TOKEN_ACCENT));
  OP_ADD(add_insert(pos, 20, 3, 24));
  OP_ADD(add_create(pos, 25, NODE_GRAPH_EDGE));
  OP_ADD(add_ref_prop(pos, 25, PROP_FROM, 22));
  OP_ADD(add_ref_prop(pos, 25, PROP_TO, 23));
  OP_ADD(add_bool_prop(pos, 25, PROP_DIRECTED, 1));
  OP_ADD(add_float_prop(pos, 25, PROP_LINE_WIDTH, 2));
  OP_ADD(add_int_prop(pos, 25, PROP_STROKE_TOKEN, TOKEN_TEXT_MUTED));
  OP_ADD(add_insert(pos, 20, 4, 25));

  /* Plan graph with statuses, costs, and annotations. */
  OP_ADD(add_create(pos, 30, NODE_PLAN_GRAPH));
  OP_ADD(add_string_prop(pos, 30, PROP_LABEL, "Query plan"));
  OP_ADD(add_insert(pos, 1, 3, 30));
  OP_ADD(add_create(pos, 31, NODE_PLAN_NODE));
  OP_ADD(add_text(pos, 31, "Seq Scan"));
  OP_ADD(add_bool_prop(pos, 31, PROP_EXPANDED, 1));
  OP_ADD(add_int_prop(pos, 31, PROP_PLAN_STATUS, PLAN_RUNNING));
  OP_ADD(add_float_prop(pos, 31, PROP_COST, 12.5));
  OP_ADD(add_insert(pos, 30, 0, 31));
  OP_ADD(add_create(pos, 32, NODE_PLAN_NODE));
  OP_ADD(add_text(pos, 32, "Index Scan"));
  OP_ADD(add_int_prop(pos, 32, PROP_PLAN_STATUS, PLAN_DONE));
  OP_ADD(add_float_prop(pos, 32, PROP_COST, 4.25));
  OP_ADD(add_string_prop(pos, 32, PROP_ANNOTATION, "actual rows 42"));
  OP_ADD(add_insert(pos, 31, 0, 32));
  OP_ADD(add_create(pos, 33, NODE_PLAN_NODE));
  OP_ADD(add_text(pos, 33, "Hash Aggregate"));
  OP_ADD(add_int_prop(pos, 33, PROP_PLAN_STATUS, PLAN_FAILED));
  OP_ADD(add_float_prop(pos, 33, PROP_COST, 30.25));
  OP_ADD(add_string_prop(pos, 33, PROP_ANNOTATION, "spilled to disk"));
  OP_ADD(add_insert(pos, 30, 1, 33));

  /* Canvas with one of every drawing primitive. */
  OP_ADD(add_create(pos, 40, NODE_CANVAS));
  OP_ADD(add_string_prop(pos, 40, PROP_LABEL, "Sketch"));
  OP_ADD(add_float_prop(pos, 40, PROP_WIDTH, 640));
  OP_ADD(add_float_prop(pos, 40, PROP_HEIGHT, 150));
  OP_ADD(add_insert(pos, 1, 4, 40));

  OP_ADD(add_create(pos, 41, NODE_CANVAS_SHAPE));
  OP_ADD(add_int_prop(pos, 41, PROP_SHAPE, CANVAS_RECT));
  OP_ADD(add_float_prop(pos, 41, PROP_X, 10));
  OP_ADD(add_float_prop(pos, 41, PROP_Y, 10));
  OP_ADD(add_float_prop(pos, 41, PROP_WIDTH, 120));
  OP_ADD(add_float_prop(pos, 41, PROP_HEIGHT, 70));
  OP_ADD(add_int_prop(pos, 41, PROP_FILL_TOKEN, TOKEN_ACCENT));
  OP_ADD(add_int_prop(pos, 41, PROP_STROKE_TOKEN, TOKEN_BORDER));
  OP_ADD(add_insert(pos, 40, 0, 41));

  OP_ADD(add_create(pos, 42, NODE_CANVAS_SHAPE));
  OP_ADD(add_int_prop(pos, 42, PROP_SHAPE, CANVAS_LINE));
  OP_ADD(add_float_prop(pos, 42, PROP_X, 160));
  OP_ADD(add_float_prop(pos, 42, PROP_Y, 20));
  OP_ADD(add_float_prop(pos, 42, PROP_WIDTH, 180));
  OP_ADD(add_float_prop(pos, 42, PROP_HEIGHT, 90));
  OP_ADD(add_int_prop(pos, 42, PROP_STROKE_TOKEN, TOKEN_TEXT));
  OP_ADD(add_float_prop(pos, 42, PROP_LINE_WIDTH, 3));
  OP_ADD(add_insert(pos, 40, 1, 42));

  OP_ADD(add_create(pos, 43, NODE_CANVAS_SHAPE));
  OP_ADD(add_int_prop(pos, 43, PROP_SHAPE, CANVAS_ELLIPSE));
  OP_ADD(add_float_prop(pos, 43, PROP_X, 380));
  OP_ADD(add_float_prop(pos, 43, PROP_Y, 20));
  OP_ADD(add_float_prop(pos, 43, PROP_WIDTH, 100));
  OP_ADD(add_float_prop(pos, 43, PROP_HEIGHT, 80));
  OP_ADD(add_int_prop(pos, 43, PROP_FILL_TOKEN, TOKEN_SUCCESS));
  OP_ADD(add_insert(pos, 40, 2, 43));

  OP_ADD(add_create(pos, 44, NODE_CANVAS_SHAPE));
  OP_ADD(add_int_prop(pos, 44, PROP_SHAPE, CANVAS_TEXT));
  OP_ADD(add_float_prop(pos, 44, PROP_X, 160));
  OP_ADD(add_float_prop(pos, 44, PROP_Y, 118));
  OP_ADD(add_float_prop(pos, 44, PROP_FONT_SIZE, 14));
  OP_ADD(add_int_prop(pos, 44, PROP_TEXT_TOKEN, TOKEN_TEXT));
  OP_ADD(add_text(pos, 44, "canvas label"));
  OP_ADD(add_insert(pos, 40, 3, 44));

  OP_ADD(add_create(pos, 45, NODE_CANVAS_SHAPE));
  OP_ADD(add_int_prop(pos, 45, PROP_SHAPE, CANVAS_IMAGE));
  OP_ADD(add_float_prop(pos, 45, PROP_X, 520));
  OP_ADD(add_float_prop(pos, 45, PROP_Y, 20));
  OP_ADD(add_float_prop(pos, 45, PROP_WIDTH, 90));
  OP_ADD(add_float_prop(pos, 45, PROP_HEIGHT, 60));
  OP_ADD(add_string_prop(pos, 45, PROP_SRC, "assets/logo.svg"));
  OP_ADD(add_string_prop(pos, 45, PROP_ALT, "logo"));
  OP_ADD(add_insert(pos, 40, 4, 45));

  /* Host-rendered rich text: styled spans, literal markup, and an inline
   * link. The ABI carries no HTML; the host draws every glyph. */
  OP_ADD(add_create(pos, 50, NODE_RICH_TEXT));
  OP_ADD(add_string_prop(pos, 50, PROP_FONT_ASSET, "assets/Go-Regular.ttf"));
  OP_ADD(add_insert(pos, 1, 5, 50));

  OP_ADD(add_create(pos, 51, NODE_TEXT_SPAN));
  OP_ADD(add_text(pos, 51, "Rich text "));
  OP_ADD(add_int_prop(pos, 51, PROP_FONT_WEIGHT, 700));
  OP_ADD(add_insert(pos, 50, 0, 51));

  OP_ADD(add_create(pos, 52, NODE_TEXT_SPAN));
  OP_ADD(add_text(pos, 52, "with <b>literal</b> markup "));
  OP_ADD(add_bool_prop(pos, 52, PROP_ITALIC, 1));
  OP_ADD(add_insert(pos, 50, 1, 52));

  OP_ADD(add_create(pos, 53, NODE_TEXT_SPAN));
  OP_ADD(add_text(pos, 53, "and a "));
  OP_ADD(add_insert(pos, 50, 2, 53));

  OP_ADD(add_create(pos, 54, NODE_TEXT_SPAN));
  OP_ADD(add_text(pos, 54, "trusted link"));
  OP_ADD(add_string_prop(pos, 54, PROP_LINK, "https://example.com/gallery"));
  OP_ADD(add_bool_prop(pos, 54, PROP_UNDERLINE, 1));
  OP_ADD(add_insert(pos, 50, 3, 54));

  /* A 250,000-item virtual list: the host pulls only the visible window. */
  OP_ADD(add_create(pos, 60, NODE_VIRTUAL_LIST));
  OP_ADD(add_string_prop(pos, 60, PROP_LABEL, "Virtual feed"));
  OP_ADD(add_int_prop(pos, 60, PROP_ITEM_COUNT, VIRTUAL_ITEM_COUNT));
  OP_ADD(add_float_prop(pos, 60, PROP_HEIGHT, 180));
  OP_ADD(add_insert(pos, 1, 6, 60));
  for (i = 0; i < INITIAL_VIRTUAL_ITEMS; i++) {
    char feed[32];
    uint32_t item_id = 61 + i;
    format_feed_row(feed, sizeof(feed), (int32_t)i);
    OP_ADD(add_create(pos, item_id, NODE_VIRTUAL_ITEM));
    OP_ADD(add_int_prop(pos, item_id, PROP_ITEM_INDEX, (int64_t)i));
    OP_ADD(add_text(pos, item_id, feed));
    OP_ADD(add_insert(pos, 60, i, item_id));
  }

  /* Packaged image: the host resolves the id inside the package. */
  OP_ADD(add_create(pos, 70, NODE_IMAGE));
  OP_ADD(add_string_prop(pos, 70, PROP_LABEL, "Packaged asset"));
  OP_ADD(add_string_prop(pos, 70, PROP_SRC, "assets/logo.svg"));
  OP_ADD(add_string_prop(pos, 70, PROP_ALT, "Image fallback"));
  OP_ADD(add_float_prop(pos, 70, PROP_WIDTH, 96));
  OP_ADD(add_float_prop(pos, 70, PROP_HEIGHT, 64));
  OP_ADD(add_insert(pos, 1, 7, 70));

  /* Host-driven transition: clicking Animate toggles the bar's width. */
  OP_ADD(add_create(pos, 80, NODE_BUTTON));
  OP_ADD(add_string_prop(pos, 80, PROP_LABEL, "Animate"));
  OP_ADD(add_insert(pos, 1, 8, 80));
  OP_ADD(add_create(pos, 81, NODE_CONTAINER));
  OP_ADD(add_float_prop(pos, 81, PROP_WIDTH, ANIMATE_NARROW));
  OP_ADD(add_float_prop(pos, 81, PROP_HEIGHT, 24));
  OP_ADD(add_int_prop(pos, 81, PROP_BACKGROUND_TOKEN, TOKEN_ACCENT));
  OP_ADD(add_int_prop(pos, 81, PROP_TRANSITION_MS, ANIMATE_DURATION_MS));
  OP_ADD(add_int_prop(pos, 81, PROP_TRANSITION_EASE, EASE_LINEAR));
  OP_ADD(add_insert(pos, 1, 9, 81));
#undef OP_ADD

  set_op_count(ops);
  status = finish_apply(pos);
  apply_status = status;
}

static int32_t listen_one(uint64_t handle, uint32_t kind) {
  uint32_t pos = 0;
  int32_t status;
  pos = put_u64(request, pos, handle);
  pos = put_u32(request, pos, kind);
  status = response_status(call_ui(UI_LISTEN, request, pos));
  if (status == 0) {
    listener_count++;
  }
  return status;
}

static void register_listeners(void) {
  int32_t status = (int32_t)STATUS_OK;
  if (apply_status != (int32_t)STATUS_OK) {
    listen_status = STATUS_NOT_RUN;
    return;
  }
  if (status == 0) {
    status = listen_one(handle_grid, EVENT_ITEM_REQUEST);
  }
  if (status == 0) {
    status = listen_one(handle_grid, EVENT_SCROLL);
  }
  if (status == 0) {
    status = listen_one(handle_grid, EVENT_SELECTION);
  }
  if (status == 0) {
    status = listen_one(handle_grid, EVENT_CHANGE);
  }
  if (status == 0) {
    status = listen_one(handle_graph, EVENT_SELECTION);
  }
  if (status == 0) {
    status = listen_one(handle_graph, EVENT_DOUBLE_CLICK);
  }
  if (status == 0) {
    status = listen_one(handle_plan, EVENT_SELECTION);
  }
  if (status == 0) {
    status = listen_one(handle_plan, EVENT_SUBMIT);
  }
  if (status == 0) {
    status = listen_one(handle_canvas, EVENT_CLICK);
  }
  if (status == 0) {
    status = listen_one(handle_canvas, EVENT_DRAG);
  }
  if (status == 0) {
    status = listen_one(handle_rich, EVENT_SELECTION);
  }
  if (status == 0) {
    status = listen_one(handle_virtual, EVENT_ITEM_REQUEST);
  }
  if (status == 0) {
    status = listen_one(handle_virtual, EVENT_CLICK);
  }
  if (status == 0) {
    status = listen_one(handle_virtual, EVENT_SELECTION);
  }
  if (status == 0) {
    status = listen_one(handle_animate, EVENT_CLICK);
  }
  if (status == 0) {
    status = listen_one(handle_animate_target, EVENT_ANIMATION_END);
  }
  listen_status = status;
}

/* probe_invalid proves the host rejects an out-of-kind property and a stale
 * handle without committing a new revision. */
static void probe_invalid(void) {
  uint32_t before = revision;
  uint32_t pos = 0;
  uint32_t ops = 0;

  if (apply_status != (int32_t)STATUS_OK) {
    return;
  }
  batch_buffer = request;
  pos = put_u64(request, pos, 0); /* keep the current root */
  pos = put_u32(request, pos, 0);
  pos = add_int_prop(pos, handle_heading, PROP_ROW_COUNT, -1);
  ops = 1;
  set_op_count(ops);
  invalid_prop_status = response_status(call_ui(UI_APPLY, request, pos));

  /* A remote URL is not a packaged asset id. */
  pos = 0;
  batch_buffer = request;
  pos = put_u64(request, pos, 0);
  pos = put_u32(request, pos, 0);
  pos = add_string_prop(pos, handle_image, PROP_SRC,
                        "https://example.com/tracker.png");
  set_op_count(1);
  invalid_asset_status = response_status(call_ui(UI_APPLY, request, pos));

  pos = 0;
  batch_buffer = request;
  pos = put_u64(request, pos, 0);
  pos = put_u32(request, pos, 0);
  pos = add_text(pos, 0xF00D000000000001ull, "stale");
  set_op_count(1);
  invalid_handle_status = response_status(call_ui(UI_APPLY, request, pos));

  revision_intact = revision == before;
}

/* respond_item_request appends at most MAX_ROWS_PER_REQUEST rows for the
 * window the host asked for. It never sends the whole 50,000-row set. */
static void respond_item_request(int32_t first, int32_t width) {
  uint32_t count;
  uint32_t i;
  uint32_t pos = 0;
  uint32_t ops = 0;
  int32_t status;

  if (first < 0 || width <= 0 || first == last_request_index) {
    return;
  }
  count = (uint32_t)width;
  if (count > MAX_ROWS_PER_REQUEST) {
    count = MAX_ROWS_PER_REQUEST;
  }
  batch_buffer = request;
  pos = put_u64(request, pos, 0); /* keep the current root */
  pos = put_u32(request, pos, 0);
  for (i = 0; i < count; i++) {
    uint32_t row_id = next_node_id;
    next_node_id += 4;
    pos = add_row(pos, row_id, first + (int32_t)i);
    pos = add_insert(pos, handle_grid, grid_children, row_id);
    grid_children++;
    grid_rows++;
    ops += ROW_NODE_OPS + ROW_INSERT_OPS;
  }
  set_op_count(ops);
  status = finish_apply(pos);
  if (status == (int32_t)STATUS_OK) {
    last_request_index = first;
    grid_requests++;
  }
}

/* respond_virtual_request appends at most MAX_VIRTUAL_ITEMS_PER_REQUEST
 * plain items for the visible window the host asked for. */
static void respond_virtual_request(int32_t first, int32_t width) {
  uint32_t count;
  uint32_t i;
  uint32_t pos = 0;
  uint32_t ops = 0;
  int32_t status;

  if (first < 0 || width <= 0 || first == virtual_last_request) {
    return;
  }
  count = (uint32_t)width;
  if (count > MAX_VIRTUAL_ITEMS_PER_REQUEST) {
    count = MAX_VIRTUAL_ITEMS_PER_REQUEST;
  }
  batch_buffer = request;
  pos = put_u64(request, pos, 0); /* keep the current root */
  pos = put_u32(request, pos, 0);
  for (i = 0; i < count; i++) {
    char feed[32];
    uint32_t item_id = next_node_id++;
    format_feed_row(feed, sizeof(feed), first + (int32_t)i);
    pos = add_create(pos, item_id, NODE_VIRTUAL_ITEM);
    pos = add_int_prop(pos, item_id, PROP_ITEM_INDEX, first + (int64_t)i);
    pos = add_text(pos, item_id, feed);
    pos = add_insert(pos, handle_virtual, virtual_children, item_id);
    virtual_children++;
    virtual_rows++;
    ops += VIRTUAL_ITEM_OPS + VIRTUAL_INSERT_OPS;
  }
  set_op_count(ops);
  status = finish_apply(pos);
  if (status == (int32_t)STATUS_OK) {
    virtual_last_request = first;
    virtual_requests++;
  }
}

/* toggle_animation patches the bar width. The host owns the transition and
 * reports its end once the bar settles; the guest never drives a frame. */
static void toggle_animation(void) {
  uint32_t pos = 0;
  int32_t status;

  animate_wide = !animate_wide;
  batch_buffer = request;
  pos = put_u64(request, pos, 0); /* keep the current root */
  pos = put_u32(request, pos, 0);
  pos = add_float_prop(pos, handle_animate_target, PROP_WIDTH,
                       animate_wide ? ANIMATE_WIDE : ANIMATE_NARROW);
  set_op_count(1);
  status = finish_apply(pos);
  if (status == (int32_t)STATUS_OK) {
    animation_changes++;
  }
}

static int32_t related_graph_node(uint64_t handle) {
  if (handle == handle_graph_load) {
    return 0;
  }
  if (handle == handle_graph_save) {
    return 1;
  }
  if (handle == handle_graph_transform) {
    return 2;
  }
  return -1;
}

/* handle_poll drains queued events and commits the patches they produce. */
static void handle_poll(void) {
  uint32_t pos = 0;
  uint32_t message_length;
  uint32_t count;
  uint32_t cursor;
  uint32_t index;
  int32_t transport;

  poll_calls++;
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
    uint64_t related;
    uint32_t text_length;
    if (cursor + 76 > RESPONSE_CAP) {
      break;
    }
    kind = read_u32(response + cursor);
    node = read_u64(response + cursor + 8);
    related = read_u64(response + cursor + 64);
    text_length = read_u32(response + cursor + 72);
    if (cursor + 76 + text_length > RESPONSE_CAP) {
      break;
    }
    if (kind == EVENT_ITEM_REQUEST) {
      if (node == handle_grid) {
        respond_item_request((int32_t)read_u32(response + cursor + 36),
                             (int32_t)read_f32(response + cursor + 56));
      } else if (node == handle_virtual) {
        respond_virtual_request((int32_t)read_u32(response + cursor + 36),
                                (int32_t)read_f32(response + cursor + 56));
      }
    } else if (kind == EVENT_SCROLL) {
      if (node == handle_grid) {
        scroll_events++;
        scroll_dy = (int32_t)read_f32(response + cursor + 52);
      }
    } else if (kind == EVENT_CHANGE) {
      if (node == handle_grid) {
        grid_sorts++;
        grid_sort_index = (int32_t)read_u32(response + cursor + 36);
        if (text_length == 3 && response[cursor + 76] == 'a' &&
            response[cursor + 77] == 's' && response[cursor + 78] == 'c') {
          grid_sort_direction = 0;
        } else if (text_length == 4 && response[cursor + 76] == 'd' &&
                   response[cursor + 77] == 'e' && response[cursor + 78] == 's' &&
                   response[cursor + 79] == 'c') {
          grid_sort_direction = 1;
        }
      }
    } else if (kind == EVENT_SELECTION) {
      if (node == handle_grid) {
        grid_selections++;
      } else if (node == handle_graph) {
        graph_selections++;
        graph_selection_index = (int32_t)read_u32(response + cursor + 36);
        graph_selection_related = related_graph_node(related);
      } else if (node == handle_plan) {
        plan_selections++;
        plan_selection_index = (int32_t)read_u32(response + cursor + 36);
      } else if (node == handle_rich) {
        rich_selections++;
        rich_selection_chars = text_length;
        copy_event_text(response + cursor + 76, text_length);
      } else if (node == handle_virtual) {
        virtual_selections++;
        virtual_selection_index = (int32_t)read_u32(response + cursor + 36);
      } else {
        plan_escaped++;
      }
    } else if (kind == EVENT_ANIMATION_END) {
      if (node == handle_animate_target) {
        animation_ends++;
      }
    } else if (kind == EVENT_DOUBLE_CLICK) {
      if (node == handle_graph) {
        graph_activations++;
      }
    } else if (kind == EVENT_SUBMIT) {
      if (node == handle_plan) {
        plan_activations++;
      } else {
        plan_escaped++;
      }
    } else if (kind == EVENT_CLICK) {
      if (node == handle_canvas) {
        canvas_clicks++;
        canvas_click_x = (int32_t)read_f32(response + cursor + 40);
        canvas_click_y = (int32_t)read_f32(response + cursor + 44);
      } else if (node == handle_virtual) {
        virtual_clicks++;
        virtual_click_index = (int32_t)read_u32(response + cursor + 36);
      } else if (node == handle_animate) {
        toggle_animation();
      }
    } else if (kind == EVENT_DRAG) {
      if (node == handle_canvas) {
        canvas_drags++;
        canvas_drag_x = (int32_t)read_f32(response + cursor + 40);
        canvas_drag_y = (int32_t)read_f32(response + cursor + 44);
        canvas_drag_dx = (int32_t)read_f32(response + cursor + 48);
        canvas_drag_dy = (int32_t)read_f32(response + cursor + 52);
      }
    }
    cursor += 76 + text_length;
  }
}

/* run_report writes the bounded state report the e2e test reads. */
static int32_t run_report(void) {
  uint32_t pos = 0;
  pos = r_text(pos, "gallery sample\n");
  pos = r_text(pos, "ui.apply status=");
  pos = r_text(pos, status_name(apply_status));
  pos = r_text(pos, " revision=");
  pos = r_u32(pos, revision);
  pos = r_text(pos, "\nui.listen status=");
  pos = r_text(pos, status_name(listen_status));
  pos = r_text(pos, " listeners=");
  pos = r_u32(pos, listener_count);
  pos = r_text(pos, "\ngrid.row_count=");
  pos = r_u32(pos, GRID_ROW_COUNT);
  pos = r_text(pos, " grid.rows=");
  pos = r_u32(pos, grid_rows);
  pos = r_text(pos, " grid.children=");
  pos = r_u32(pos, grid_children);
  pos = r_text(pos, " grid.requests=");
  pos = r_u32(pos, grid_requests);
  pos = r_text(pos, " grid.selection=");
  pos = r_u32(pos, grid_selections);
  pos = r_text(pos, " grid.sort=");
  pos = r_u32(pos, grid_sorts);
  pos = r_text(pos, " grid.sort.index=");
  pos = r_i32(pos, grid_sort_index);
  pos = r_text(pos, " grid.sort.direction=");
  pos = r_i32(pos, grid_sort_direction);
  pos = r_text(pos, "\ngraph.selection=");
  pos = r_u32(pos, graph_selections);
  pos = r_text(pos, " graph.selection.index=");
  pos = r_i32(pos, graph_selection_index);
  pos = r_text(pos, " graph.selection.related=");
  pos = r_i32(pos, graph_selection_related);
  pos = r_text(pos, " graph.activation=");
  pos = r_u32(pos, graph_activations);
  pos = r_text(pos, "\nplan.selection=");
  pos = r_u32(pos, plan_selections);
  pos = r_text(pos, " plan.selection.index=");
  pos = r_i32(pos, plan_selection_index);
  pos = r_text(pos, " plan.activation=");
  pos = r_u32(pos, plan_activations);
  pos = r_text(pos, " plan.escaped=");
  pos = r_u32(pos, plan_escaped);
  pos = r_text(pos, "\ncanvas.click=");
  pos = r_u32(pos, canvas_clicks);
  pos = r_text(pos, " canvas.click.x=");
  pos = r_i32(pos, canvas_click_x);
  pos = r_text(pos, " canvas.click.y=");
  pos = r_i32(pos, canvas_click_y);
  pos = r_text(pos, "\ncanvas.drag=");
  pos = r_u32(pos, canvas_drags);
  pos = r_text(pos, " canvas.drag.x=");
  pos = r_i32(pos, canvas_drag_x);
  pos = r_text(pos, " canvas.drag.y=");
  pos = r_i32(pos, canvas_drag_y);
  pos = r_text(pos, " canvas.drag.dx=");
  pos = r_i32(pos, canvas_drag_dx);
  pos = r_text(pos, " canvas.drag.dy=");
  pos = r_i32(pos, canvas_drag_dy);
  pos = r_text(pos, "\nevents.scroll=");
  pos = r_u32(pos, scroll_events);
  pos = r_text(pos, " events.scroll.dy=");
  pos = r_i32(pos, scroll_dy);
  pos = r_text(pos, "\nrich.selection=");
  pos = r_u32(pos, rich_selections);
  pos = r_text(pos, " rich.selection.chars=");
  pos = r_u32(pos, rich_selection_chars);
  pos = r_text(pos, " rich.selection.text=");
  pos = r_text(pos, rich_selection_text);
  pos = r_text(pos, "\nvirtual.item_count=");
  pos = r_u32(pos, VIRTUAL_ITEM_COUNT);
  pos = r_text(pos, " virtual.rows=");
  pos = r_u32(pos, virtual_rows);
  pos = r_text(pos, " virtual.requests=");
  pos = r_u32(pos, virtual_requests);
  pos = r_text(pos, " virtual.click=");
  pos = r_u32(pos, virtual_clicks);
  pos = r_text(pos, " virtual.click.index=");
  pos = r_i32(pos, virtual_click_index);
  pos = r_text(pos, " virtual.selection=");
  pos = r_u32(pos, virtual_selections);
  pos = r_text(pos, " virtual.selection.index=");
  pos = r_i32(pos, virtual_selection_index);
  pos = r_text(pos, "\nanimation.changes=");
  pos = r_u32(pos, animation_changes);
  pos = r_text(pos, " animation.ends=");
  pos = r_u32(pos, animation_ends);
  pos = r_text(pos, " animation.wide=");
  pos = r_u32(pos, animate_wide ? 1u : 0u);
  pos = r_text(pos, "\ninvalid.prop=");
  pos = r_text(pos, status_name(invalid_prop_status));
  pos = r_text(pos, " invalid.asset=");
  pos = r_text(pos, status_name(invalid_asset_status));
  pos = r_text(pos, " invalid.handle=");
  pos = r_text(pos, status_name(invalid_handle_status));
  pos = r_text(pos, " revision.intact=");
  pos = r_u32(pos, revision_intact ? 1u : 0u);
  pos = r_text(pos, "\npoll.calls=");
  pos = r_u32(pos, poll_calls);
  pos = r_text(pos, " handles.grid=");
  pos = r_u32(pos, handle_grid != 0 ? 1u : 0u);
  pos = r_text(pos, " handles.graph=");
  pos = r_u32(pos, handle_graph != 0 ? 1u : 0u);
  pos = r_text(pos, " handles.plan=");
  pos = r_u32(pos, handle_plan != 0 ? 1u : 0u);
  pos = r_text(pos, " handles.canvas=");
  pos = r_u32(pos, handle_canvas != 0 ? 1u : 0u);
  pos = r_text(pos, "\nreport.done\n");
  return host_output_write(report, (int32_t)pos) < 0 ? -1 : 0;
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  apply_initial();
  register_listeners();
  probe_invalid();
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
