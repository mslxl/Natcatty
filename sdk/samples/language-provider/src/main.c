/*
 * Language-provider sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It contributes one
 * `language.provider` for the built-in SQL language and implements the
 * host-to-guest language exports:
 *
 *   natcatty_language_request  receives a full document snapshot as JSON
 *   natcatty_language_cancel   best-effort cooperative cancellation export
 *
 * and imports the host replies:
 *
 *   language_respond           returns exactly one data-only result
 *   language_cancelled         polls whether the host superseded the request
 *
 * The provider demonstrates completion and diagnostics in the editor:
 * completion returns two bounded items, and diagnostics reports every TODO
 * marker in the document with its line and column. Every other capability is
 * declared by the manifest and answers with an empty, well-formed result.
 * Workspace indexing stays internal to the provider: the host only routes the
 * snapshot by document id and version.
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

IMPORT("natcatty_host_v1", "language_respond") extern int32_t host_language_respond(uint64_t request_id, const uint8_t *ptr, int32_t len);
IMPORT("natcatty_host_v1", "language_cancelled") extern int32_t host_language_cancelled(uint64_t request_id);

/* Stable LanguageKind wire values from the generated IDL. */
#define KIND_HIGHLIGHT 1
#define KIND_COMPLETION 2
#define KIND_DIAGNOSTICS 3
#define KIND_FORMATTING 4
#define KIND_HOVER 5
#define KIND_SIGNATURE_HELP 6
#define KIND_NAVIGATION 7
#define KIND_CODE_ACTIONS 8

#define CONTENT_CAP 65536u
#define REQUEST_CAP (1u << 20)
#define OUTPUT_CAP 16384u
#define HEAP_CAP (1u << 20)
#define MAX_DIAGNOSTICS 8u

static uint8_t heap[HEAP_CAP];
static uint32_t heap_used;
static uint8_t content[CONTENT_CAP];
static uint32_t content_len;
static uint8_t output[OUTPUT_CAP];

/* The current request buffer, set by natcatty_language_request. */
static const uint8_t *request;
static uint32_t request_len;

static uint32_t cstr_len(const char *text) {
  uint32_t length = 0;
  while (text[length] != '\0') {
    length++;
  }
  return length;
}

static int bytes_equal(const uint8_t *data, const char *text, uint32_t length) {
  uint32_t i;
  for (i = 0; i < length; i++) {
    if (data[i] != (uint8_t)text[i]) {
      return 0;
    }
  }
  return 1;
}

/* field_offset returns the offset just after `"name":` in the request. */
static int field_offset(const char *name, uint32_t *out) {
  uint32_t name_len = cstr_len(name);
  uint32_t i;
  if (request_len < name_len + 4u) {
    return 0;
  }
  for (i = 0; i + name_len + 4u <= request_len; i++) {
    if (request[i] != '"') {
      continue;
    }
    if (!bytes_equal(request + i + 1u, name, name_len)) {
      continue;
    }
    if (request[i + 1u + name_len] != '"' ||
        request[i + 2u + name_len] != ':') {
      continue;
    }
    *out = i + 3u + name_len;
    return 1;
  }
  return 0;
}

static void skip_space(uint32_t *pos) {
  while (*pos < request_len &&
         (request[*pos] == ' ' || request[*pos] == '\t' ||
          request[*pos] == '\r' || request[*pos] == '\n')) {
    (*pos)++;
  }
}

static int parse_u64(uint32_t pos, uint64_t *out) {
  uint64_t value = 0;
  uint32_t digits = 0;
  skip_space(&pos);
  while (pos < request_len && request[pos] >= '0' && request[pos] <= '9') {
    value = value * 10u + (uint64_t)(request[pos] - '0');
    pos++;
    digits++;
  }
  if (digits == 0) {
    return 0;
  }
  *out = value;
  return 1;
}

static int hex_value(uint8_t c) {
  if (c >= '0' && c <= '9') {
    return c - '0';
  }
  if (c >= 'a' && c <= 'f') {
    return c - 'a' + 10;
  }
  if (c >= 'A' && c <= 'F') {
    return c - 'A' + 10;
  }
  return -1;
}

/* parse_string_field decodes a JSON string field into out, returning its
 * length. The sample only needs plain document text; escaped control bytes
 * become spaces and non-ASCII \u escapes become '?'. */
static uint32_t parse_string_field(const char *name) {
  uint32_t pos;
  uint32_t written = 0;
  if (!field_offset(name, &pos)) {
    return 0;
  }
  skip_space(&pos);
  if (pos >= request_len || request[pos] != '"') {
    return 0;
  }
  pos++;
  while (pos < request_len && written + 1u < CONTENT_CAP) {
    uint8_t c = request[pos];
    if (c == '"') {
      break;
    }
    if (c == '\\' && pos + 1u < request_len) {
      uint8_t escape = request[pos + 1u];
      pos += 2u;
      switch (escape) {
        case '"': c = '"'; break;
        case '\\': c = '\\'; break;
        case '/': c = '/'; break;
        case 'b': c = ' '; break;
        case 'f': c = ' '; break;
        case 'n': c = '\n'; break;
        case 'r': c = '\r'; break;
        case 't': c = '\t'; break;
        case 'u': {
          int high, low;
          if (pos + 4u > request_len ||
              (high = hex_value(request[pos])) < 0 ||
              (low = hex_value(request[pos + 1u])) < 0) {
            c = '?';
            pos += 4u;
            break;
          }
          /* Only ASCII survives; the sample never renders exotic text. */
          c = (uint8_t)((high << 4) | low);
          if (c >= 0x80) {
            c = '?';
          }
          pos += 4u;
          break;
        }
        default: c = '?'; break;
      }
      content[written++] = c;
      continue;
    }
    content[written++] = c;
    pos++;
  }
  content_len = written;
  return written;
}

/* find_bytes returns the offset just after needle. */
static int find_bytes(const char *needle, uint32_t *out) {
  uint32_t length = cstr_len(needle);
  uint32_t i;
  if (request_len < length) {
    return 0;
  }
  for (i = 0; i + length <= request_len; i++) {
    if (bytes_equal(request + i, needle, length)) {
      *out = i + length;
      return 1;
    }
  }
  return 0;
}

static void parse_cursor(uint32_t *line, uint32_t *column) {
  uint32_t pos;
  uint64_t value;
  *line = 0;
  *column = 0;
  if (!find_bytes("\"cursor\":{\"line\":", &pos) || !parse_u64(pos, &value)) {
    return;
  }
  *line = (uint32_t)value;
  if (find_bytes("\"column\":", &pos) && parse_u64(pos, &value)) {
    *column = (uint32_t)value;
  }
}

static uint32_t o_text(uint32_t pos, const char *text) {
  uint32_t i = 0;
  while (text[i] != '\0' && pos + 1u < OUTPUT_CAP) {
    output[pos++] = (uint8_t)text[i++];
  }
  return pos;
}

static uint32_t o_u64(uint32_t pos, uint64_t value) {
  uint8_t digits[20];
  uint32_t count = 0;
  if (value == 0) {
    digits[count++] = (uint8_t)'0';
  }
  while (value > 0 && count < sizeof(digits)) {
    digits[count++] = (uint8_t)('0' + (value % 10u));
    value /= 10u;
  }
  while (count > 0 && pos + 1u < OUTPUT_CAP) {
    output[pos++] = digits[--count];
  }
  return pos;
}

static uint32_t o_u32(uint32_t pos, uint32_t value) {
  return o_u64(pos, (uint64_t)value);
}

static uint32_t open_response(uint32_t pos, uint64_t version) {
  pos = o_text(pos, "{\"code\":\"ok\",\"version\":");
  pos = o_u64(pos, version);
  return o_text(pos, ",\"data\":");
}

static uint32_t completion_data(uint32_t pos) {
  return o_text(pos,
      "{\"items\":["
      "{\"label\":\"sample_keyword\",\"insertText\":\"sample_keyword\","
      "\"kind\":\"keyword\",\"detail\":\"provided by acme.language-provider\","
      "\"sortKey\":\"a\"},"
      "{\"label\":\"sample_function\",\"insertText\":\"sample_function(\","
      "\"kind\":\"function\",\"detail\":\"provided by acme.language-provider\","
      "\"documentation\":\"A completion supplied by the language-provider sample.\","
      "\"sortKey\":\"b\"}"
      "]}");
}

static uint32_t diagnostics_data(uint32_t pos) {
  uint32_t i;
  uint32_t line = 0;
  uint32_t column = 0;
  uint32_t count = 0;
  pos = o_text(pos, "{\"diagnostics\":[");
  for (i = 0; i + 4u <= content_len && count < MAX_DIAGNOSTICS; i++) {
    if (content[i] == 'T' && bytes_equal(content + i, "TODO", 4)) {
      if (count > 0) {
        pos = o_text(pos, ",");
      }
      pos = o_text(pos, "{\"range\":{\"start\":{\"line\":");
      pos = o_u32(pos, line);
      pos = o_text(pos, ",\"column\":");
      pos = o_u32(pos, column);
      pos = o_text(pos, "},\"end\":{\"line\":");
      pos = o_u32(pos, line);
      pos = o_text(pos, ",\"column\":");
      pos = o_u32(pos, column + 4u);
      pos = o_text(pos,
          "}},\"severity\":2,\"code\":\"sample.todo\","
          "\"message\":\"TODO marker found by acme.language-provider\","
          "\"source\":\"acme.language-provider\"}");
      count++;
      i += 3u;
      continue;
    }
    if (content[i] == '\n') {
      line++;
      column = 0;
    } else {
      column++;
    }
  }
  return o_text(pos, "]}");
}

static int32_t respond(uint64_t request_id, uint64_t version, uint32_t kind) {
  uint32_t pos = 0;
  pos = open_response(pos, version);
  switch (kind) {
    case KIND_COMPLETION:
      pos = completion_data(pos);
      break;
    case KIND_DIAGNOSTICS:
      pos = diagnostics_data(pos);
      break;
    case KIND_HIGHLIGHT:
      pos = o_text(pos, "{\"highlights\":[]}");
      break;
    case KIND_FORMATTING:
      pos = o_text(pos, "{\"edits\":[]}");
      break;
    case KIND_HOVER:
      pos = o_text(pos,
          "{\"hover\":{\"contents\":\"Language-provider sample hover\","
          "\"format\":\"plain\"}}");
      break;
    case KIND_SIGNATURE_HELP:
      pos = o_text(pos, "{\"signatures\":[]}");
      break;
    case KIND_NAVIGATION:
      pos = o_text(pos, "{\"locations\":[]}");
      break;
    case KIND_CODE_ACTIONS:
      pos = o_text(pos, "{\"actions\":[]}");
      break;
    default:
      pos = 0;
      pos = o_text(pos, "{\"code\":\"invalid\",\"message\":"
          "\"unsupported language capability\",\"version\":");
      pos = o_u64(pos, version);
      pos = o_text(pos, "}");
      return host_language_respond(request_id, output, (int32_t)pos) < 0 ? -1 : 0;
  }
  pos = o_text(pos, "}");
  return host_language_respond(request_id, output, (int32_t)pos) < 0 ? -1 : 0;
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  uint32_t ptr;
  if (size > HEAP_CAP || heap_used > HEAP_CAP - size) {
    return 0;
  }
  ptr = (uint32_t)heap + heap_used;
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

EXPORT("natcatty_language_request") int32_t natcatty_language_request(
    uint64_t request_id, int32_t kind, uint32_t ptr, uint32_t len) {
  uint64_t version = 0;
  uint32_t pos;

  if (len > REQUEST_CAP) {
    return -1;
  }
  request = (const uint8_t *)ptr;
  request_len = len;

  /* Cooperative cancellation: if the host superseded this request, the guest
   * answers nothing. The host has already discarded the slot. */
  if (host_language_cancelled(request_id) != 0) {
    return 0;
  }

  content_len = 0;
  if (field_offset("version", &pos)) {
    (void)parse_u64(pos, &version);
  }
  (void)parse_string_field("content");
  /* Cursor position is part of the snapshot contract even though this
   * provider's completion list does not depend on it. */
  {
    uint32_t line = 0;
    uint32_t column = 0;
    parse_cursor(&line, &column);
    (void)line;
    (void)column;
  }
  return respond(request_id, version, (uint32_t)kind);
}

/* The optional cancel export cannot run while the single request call is in
 * flight; the language_cancelled import is the observable path. The export
 * exists to exercise the generated optional-export surface. */
EXPORT("natcatty_language_cancel") int32_t natcatty_language_cancel(
    uint64_t request_id) {
  (void)request_id;
  return 0;
}
