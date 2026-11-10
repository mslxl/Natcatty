/*
 * A guest that makes one host-service call and then reports success. The Go
 * test replaces the dispatcher with one that blocks, so the fixture proves a
 * host-service call may outlive the guest's ordinary compute slice and that
 * the guest resumes with the budget reset afterwards.
 *
 * Built with clang --target=wasm32 and wasm-ld; see gen.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);
IMPORT("natcatty:svc/storage_v1", "call") extern int32_t storage_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

static uint8_t heap[65536];
static uint32_t used;
static uint8_t response[256];

static int command_contains(uint32_t command_ptr, uint32_t command_len, const char *needle) {
  const uint8_t *command = (const uint8_t *)command_ptr;
  uint32_t length = 0;
  uint32_t i;
  while (needle[length] != '\0') {
    length++;
  }
  if (length == 0 || command_len < length) {
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
  uint32_t ptr = (uint32_t)heap + used;
  used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  static const uint8_t request[] = "{\"key\":\"slow\"}";
  static const uint8_t message[] = "slow ok";
  (void)command_ptr;
  (void)command_len;

  /* Method 1 is storage.get. A blocking dispatcher returns an ok envelope. */
  if (storage_call(1, request, sizeof(request) - 1, response, sizeof(response)) != 0) {
    return -1;
  }
  if (response[0] != '{') {
    return -2;
  }
  /* The "loop" command proves ordinary guest compute stays bounded after the
   * host work returned and the compute budget resumed. */
  if (command_contains(command_ptr, command_len, "loop")) {
    for (;;) {
    }
  }
  return host_output_write(message, sizeof(message) - 1) < 0 ? -3 : 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
