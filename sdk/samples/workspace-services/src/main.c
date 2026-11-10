/*
 * Workspace-services sample guest.
 *
 * This is a real WASM plugin built against the v1 ABI. It imports the files,
 * hosts, sessions, and terminal host services and exercises:
 *
 *   files       preopens, open, range write, range read, stat, close
 *   hosts       list, get, create (denied), test/grant scope denials
 *   sessions    list, get, and an out-of-scope get denial
 *   terminal    observe, lossy read, close, plus separately scoped
 *               write and info denials
 *   files.remote read/write denials on a Host-scoped permission
 *
 * The report is one bounded output_write. With the optional grants the report
 * carries successful reads and values; without them every top-level call is a
 * typed `denied` carrying the exact permission, which is the point of the
 * sample. The granted directory is discovered through files.preopens rather
 * than hard-coded, so the guest never needs to know the host's absolute path.
 *
 * Built with clang --target=wasm32 and wasm-ld; see build.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
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

IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);

IMPORT("natcatty:svc/files_v1", "call") extern int32_t files_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/hosts_v1", "call") extern int32_t hosts_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/sessions_v1", "call") extern int32_t sessions_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/terminal_v1", "call") extern int32_t terminal_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

/* Stable method ids from the generated IDL. */
#define FILES_OPEN 1u
#define FILES_READ 2u
#define FILES_WRITE 3u
#define FILES_STAT 6u
#define FILES_CLOSE 9u
#define FILES_PREOPENS 12u
#define FILES_REMOTE_LIST 14u
#define FILES_REMOTE_WRITE 17u

#define HOSTS_LIST 1u
#define HOSTS_GET 2u
#define HOSTS_CREATE 3u

#define SESSIONS_LIST 1u
#define SESSIONS_GET 2u

#define TERMINAL_OBSERVE 1u
#define TERMINAL_READ 2u
#define TERMINAL_CLOSE 3u
#define TERMINAL_WRITE 4u
#define TERMINAL_INFO 5u

#define SVC_FILES 0u
#define SVC_HOSTS 1u
#define SVC_SESSIONS 2u
#define SVC_TERMINAL 3u

#define RESPONSE_CAP 4096u
#define REPORT_CAP 16384u

static uint8_t heap[65536];
static uint32_t heap_used;
static uint8_t response[RESPONSE_CAP];
static uint8_t report[REPORT_CAP];

static char file_handle[32];
static char terminal_handle[32];

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

static int32_t call_service(uint32_t service, uint32_t method, const char *request,
                            uint32_t request_len) {
  uint32_t i;
  for (i = 0; i < RESPONSE_CAP; i++) {
    response[i] = 0;
  }
  switch (service) {
    case SVC_FILES:
      return files_call(method, (const uint8_t *)request, request_len,
                        response, RESPONSE_CAP - 1u);
    case SVC_HOSTS:
      return hosts_call(method, (const uint8_t *)request, request_len,
                        response, RESPONSE_CAP - 1u);
    case SVC_SESSIONS:
      return sessions_call(method, (const uint8_t *)request, request_len,
                           response, RESPONSE_CAP - 1u);
    default:
      return terminal_call(method, (const uint8_t *)request, request_len,
                           response, RESPONSE_CAP - 1u);
  }
}

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

static int field_number(const char *field, uint32_t *out) {
  uint32_t name_len = text_len(field);
  uint32_t i = 0;
  while (response[i] != 0) {
    if (response[i] == '"' && bytes_start_with(response + i + 1, field, name_len) &&
        response[i + 1 + name_len] == '"' && response[i + 2 + name_len] == ':' &&
        response[i + 3 + name_len] >= '0' && response[i + 3 + name_len] <= '9') {
      uint32_t j = i + 3 + name_len;
      uint32_t number = 0;
      while (response[j] >= '0' && response[j] <= '9') {
        number = number * 10u + (uint32_t)(response[j] - '0');
        j++;
      }
      *out = number;
      return 1;
    }
    i++;
  }
  return 0;
}

static uint32_t emit_result(uint32_t pos, const char *label, int32_t status,
                            const char *value_field) {
  char value[256];
  uint32_t number;
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
  if (value_field != 0) {
    if (field_value(value_field, value, sizeof(value))) {
      pos = r_text(pos, " ");
      pos = r_text(pos, value_field);
      pos = r_text(pos, "=");
      pos = r_text(pos, value);
    } else if (field_number(value_field, &number)) {
      pos = r_text(pos, " ");
      pos = r_text(pos, value_field);
      pos = r_text(pos, "=");
      pos = r_u32(pos, number);
    }
  }
  pos = r_text(pos, "\n");
  return pos;
}

static uint32_t build_open_request(char *buffer, uint32_t cap, const char *path,
                                   const char *mode) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"path\":\"";
  static const char middle[] = "\",\"mode\":\"";
  static const char suffix[] = "\"}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) buffer[pos++] = prefix[i++];
  i = 0;
  while (path[i] != '\0' && pos + 1 < cap) buffer[pos++] = path[i++];
  i = 0;
  while (middle[i] != '\0' && pos + 1 < cap) buffer[pos++] = middle[i++];
  i = 0;
  while (mode[i] != '\0' && pos + 1 < cap) buffer[pos++] = mode[i++];
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) buffer[pos++] = suffix[i++];
  buffer[pos] = '\0';
  return pos;
}

static uint32_t build_path_request(char *buffer, uint32_t cap, const char *path) {
  return build_open_request(buffer, cap, path, "read");
}

static uint32_t build_handle_request(char *buffer, uint32_t cap, const char *handle) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"handle\":\"";
  static const char suffix[] = "\"}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) buffer[pos++] = prefix[i++];
  i = 0;
  while (handle[i] != '\0' && pos + 1 < cap) buffer[pos++] = handle[i++];
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) buffer[pos++] = suffix[i++];
  buffer[pos] = '\0';
  return pos;
}

/* build_id_request targets the hosts and sessions methods that take {"id":...}. */
static uint32_t build_id_request(char *buffer, uint32_t cap, const char *id) {
  return build_handle_request(buffer, cap, id);
}

static uint32_t build_write_request(char *buffer, uint32_t cap, const char *handle,
                                    const char *base64) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"handle\":\"";
  static const char middle[] = "\",\"offset\":0,\"data\":\"";
  static const char suffix[] = "\"}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) buffer[pos++] = prefix[i++];
  i = 0;
  while (handle[i] != '\0' && pos + 1 < cap) buffer[pos++] = handle[i++];
  i = 0;
  while (middle[i] != '\0' && pos + 1 < cap) buffer[pos++] = middle[i++];
  i = 0;
  while (base64[i] != '\0' && pos + 1 < cap) buffer[pos++] = base64[i++];
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) buffer[pos++] = suffix[i++];
  buffer[pos] = '\0';
  return pos;
}

static uint32_t build_read_request(char *buffer, uint32_t cap, const char *handle,
                                   uint32_t length) {
  uint32_t pos = 0;
  static const char prefix[] = "{\"handle\":\"";
  static const char middle[] = "\",\"offset\":0,\"length\":";
  static const char suffix[] = "}";
  uint32_t i = 0;
  while (prefix[i] != '\0' && pos + 1 < cap) buffer[pos++] = prefix[i++];
  i = 0;
  while (handle[i] != '\0' && pos + 1 < cap) buffer[pos++] = handle[i++];
  i = 0;
  while (middle[i] != '\0' && pos + 1 < cap) buffer[pos++] = middle[i++];
  {
    uint8_t digits[10];
    uint32_t count = 0;
    if (length == 0) digits[count++] = (uint8_t)'0';
    while (length > 0) {
      digits[count++] = (uint8_t)('0' + (length % 10u));
      length /= 10u;
    }
    while (count > 0 && pos + 1 < cap) buffer[pos++] = (char)digits[--count];
  }
  i = 0;
  while (suffix[i] != '\0' && pos + 1 < cap) buffer[pos++] = suffix[i++];
  buffer[pos] = '\0';
  return pos;
}

static uint32_t build_remote_write_request(char *buffer, uint32_t cap) {
  static const char request[] =
      "{\"host\":\"host-a\",\"path\":\"/sample.txt\",\"offset\":0,\"data\":\"eA==\"}";
  uint32_t i = 0;
  while (request[i] != '\0' && i + 1 < cap) {
    buffer[i] = request[i];
    i++;
  }
  buffer[i] = '\0';
  return i;
}

static int32_t run_selftest(void) {
  static const char preopens_request[] = "{}";
  static const char sample_path[] = "sample.txt";
  static const char outside_path[] = "/natcatty-not-granted/sample.txt";
  static const char glob_path[] = "*.txt";
  static const char sample_base64[] = "d29ya3NwYWNlIHNhbXBsZQ==";
  static const char terminal_base64[] = "d29ya3NwYWNlIHRlcm1pbmFsCg==";
  static const char hosts_list_request[] = "{}";
  static const char sessions_list_request[] = "{}";
  static const char host_a_request[] = "{\"id\":\"host-a\"}";
  static const char host_b_request[] = "{\"id\":\"host-b\"}";
  static const char host_create_request[] =
      "{\"label\":\"Plugin Host\",\"hostname\":\"plugin.example\","
      "\"port\":22,\"protocol\":\"ssh\"}";
  static const char session_one_request[] = "{\"id\":\"session-1\"}";
  static const char session_two_request[] = "{\"id\":\"session-2\"}";
  static const char observe_request[] = "{\"sessionId\":\"session-1\"}";
  static const char terminal_info_request[] = "{\"sessionId\":\"session-1\"}";
  static const char terminal_write_request[] =
      "{\"sessionId\":\"session-1\",\"data\":\"bHMK\"}";
  static const char remote_list_request[] = "{\"host\":\"host-a\",\"path\":\"/\"}";
  char value[256];
  char handle[32];
  char request[256];
  uint32_t pos = 0;
  uint32_t length;
  int32_t status;

  /* Local files: the guest discovers its granted preopen and uses paths
   * relative to it. No absolute host path is compiled into the guest. */
  status = call_service(SVC_FILES, FILES_PREOPENS, preopens_request,
                        sizeof(preopens_request) - 1);
  pos = emit_result(pos, "files.preopens", status, 0);

  file_handle[0] = '\0';
  length = build_open_request(request, sizeof(request), sample_path, "read-write");
  status = call_service(SVC_FILES, FILES_OPEN, request, length);
  pos = emit_result(pos, "files.open", status, "handle");
  if (status == 0 && field_value("handle", value, sizeof(value))) {
    text_copy(file_handle, sizeof(file_handle), value);
  }

  if (file_handle[0] != '\0') {
    length = build_write_request(request, sizeof(request), file_handle, sample_base64);
    status = call_service(SVC_FILES, FILES_WRITE, request, length);
    pos = emit_result(pos, "files.write", status, "written");

    length = build_read_request(request, sizeof(request), file_handle, 16);
    status = call_service(SVC_FILES, FILES_READ, request, length);
    pos = emit_result(pos, "files.read", status, "data");
    if (status == 0 && field_value("data", value, sizeof(value)) &&
        text_eq(value, sample_base64)) {
      pos = r_text(pos, "files.read.match=ok\n");
    } else {
      pos = r_text(pos, "files.read.match=no\n");
    }

    length = build_path_request(request, sizeof(request), sample_path);
    status = call_service(SVC_FILES, FILES_STAT, request, length);
    pos = emit_result(pos, "files.stat", status, 0);

    length = build_handle_request(request, sizeof(request), file_handle);
    status = call_service(SVC_FILES, FILES_CLOSE, request, length);
    pos = emit_result(pos, "files.close", status, 0);
  } else {
    pos = r_text(pos, "files.write skipped=no-open\n");
    pos = r_text(pos, "files.read skipped=no-open\n");
    pos = r_text(pos, "files.stat skipped=no-open\n");
  }

  length = build_open_request(request, sizeof(request), outside_path, "read");
  status = call_service(SVC_FILES, FILES_OPEN, request, length);
  pos = emit_result(pos, "files.open.outside", status, 0);

  length = build_open_request(request, sizeof(request), glob_path, "read");
  status = call_service(SVC_FILES, FILES_OPEN, request, length);
  pos = emit_result(pos, "files.open.glob", status, 0);

  /* Hosts: list and one granted get; the second Host and every mutating call
   * are denied by the exact Host scope. */
  status = call_service(SVC_HOSTS, HOSTS_LIST, hosts_list_request,
                        sizeof(hosts_list_request) - 1);
  pos = emit_result(pos, "hosts.list", status, 0);

  status = call_service(SVC_HOSTS, HOSTS_GET, host_a_request,
                        sizeof(host_a_request) - 1);
  pos = emit_result(pos, "hosts.get.granted", status, 0);

  status = call_service(SVC_HOSTS, HOSTS_GET, host_b_request,
                        sizeof(host_b_request) - 1);
  pos = emit_result(pos, "hosts.get.denied", status, 0);

  status = call_service(SVC_HOSTS, HOSTS_CREATE, host_create_request,
                        sizeof(host_create_request) - 1);
  pos = emit_result(pos, "hosts.create", status, 0);

  /* Sessions: list and the granted Session; session-2 is outside the scope. */
  status = call_service(SVC_SESSIONS, SESSIONS_LIST, sessions_list_request,
                        sizeof(sessions_list_request) - 1);
  pos = emit_result(pos, "sessions.list", status, 0);

  status = call_service(SVC_SESSIONS, SESSIONS_GET, session_one_request,
                        sizeof(session_one_request) - 1);
  pos = emit_result(pos, "sessions.get.granted", status, 0);

  status = call_service(SVC_SESSIONS, SESSIONS_GET, session_two_request,
                        sizeof(session_two_request) - 1);
  pos = emit_result(pos, "sessions.get.denied", status, 0);

  /* Terminal: observation is a read-only tap; write and info have their own
   * permissions and are denied here. */
  terminal_handle[0] = '\0';
  status = call_service(SVC_TERMINAL, TERMINAL_OBSERVE, observe_request,
                        sizeof(observe_request) - 1);
  pos = emit_result(pos, "terminal.observe", status, "handle");
  if (status == 0 && field_value("handle", value, sizeof(value))) {
    text_copy(terminal_handle, sizeof(terminal_handle), value);
  }
  if (terminal_handle[0] != '\0') {
    length = build_read_request(request, sizeof(request), terminal_handle, 64);
    status = call_service(SVC_TERMINAL, TERMINAL_READ, request, length);
    pos = emit_result(pos, "terminal.read", status, "data");
    if (status == 0 && field_value("data", value, sizeof(value)) &&
        text_eq(value, terminal_base64)) {
      pos = r_text(pos, "terminal.read.match=ok\n");
    } else {
      pos = r_text(pos, "terminal.read.match=no\n");
    }
    length = build_handle_request(request, sizeof(request), terminal_handle);
    status = call_service(SVC_TERMINAL, TERMINAL_CLOSE, request, length);
    pos = emit_result(pos, "terminal.close", status, 0);
  } else {
    pos = r_text(pos, "terminal.read skipped=no-observe\n");
  }

  status = call_service(SVC_TERMINAL, TERMINAL_WRITE, terminal_write_request,
                        sizeof(terminal_write_request) - 1);
  pos = emit_result(pos, "terminal.write", status, 0);

  status = call_service(SVC_TERMINAL, TERMINAL_INFO, terminal_info_request,
                        sizeof(terminal_info_request) - 1);
  pos = emit_result(pos, "terminal.info", status, 0);

  /* Remote files are Host-scoped permissions this sample does not request. */
  status = call_service(SVC_FILES, FILES_REMOTE_LIST, remote_list_request,
                        sizeof(remote_list_request) - 1);
  pos = emit_result(pos, "files.remote_list", status, 0);

  length = build_remote_write_request(request, sizeof(request));
  status = call_service(SVC_FILES, FILES_REMOTE_WRITE, request, length);
  pos = emit_result(pos, "files.remote_write", status, 0);

  pos = r_text(pos, "selftest.done\n");
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
  if (command_contains(command_ptr, command_len, "selftest")) {
    return run_selftest();
  }
  return -1;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
