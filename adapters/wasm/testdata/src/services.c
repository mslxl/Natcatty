/*
 * A guest that imports every v1 host-service module. It calls one method on
 * each module through the shared `call` ABI, verifies that the response
 * envelope was copied into guest memory, and proves the adapter reports a
 * response-cap violation as the limit status. The Go test supplies a fake
 * dispatcher, so this fixture exercises transport plumbing rather than the
 * application handlers.
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
IMPORT("natcatty:svc/clipboard_v1", "call") extern int32_t clipboard_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/notifications_v1", "call") extern int32_t notifications_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/events_v1", "call") extern int32_t events_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);
IMPORT("natcatty:svc/settings_v1", "call") extern int32_t settings_call(uint32_t method, const uint8_t *req, uint32_t req_len, uint8_t *resp, uint32_t resp_cap);

static uint8_t heap[65536];
static uint32_t used;
static uint8_t response[256];

/* call_ok checks the transport status and that the dispatcher's JSON envelope
 * reached guest memory. */
static int32_t call_ok(int32_t status) {
  return status == 0 && response[0] == '{';
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  uint32_t ptr = (uint32_t)heap + used;
  used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  static const uint8_t set_request[] = "{\"key\":\"k\",\"value\":\"aGk=\"}";
  static const uint8_t text_request[] = "{\"text\":\"hi\"}";
  static const uint8_t empty_request[] = "{}";
  static const uint8_t message[] = "services ok";
  (void)command_ptr;
  (void)command_len;

  if (!call_ok(storage_call(2, set_request, sizeof(set_request) - 1,
                            response, sizeof(response)))) {
    return -1;
  }
  if (!call_ok(clipboard_call(2, text_request, sizeof(text_request) - 1,
                              response, sizeof(response)))) {
    return -2;
  }
  if (!call_ok(notifications_call(1, empty_request, sizeof(empty_request) - 1,
                                  response, sizeof(response)))) {
    return -3;
  }
  if (!call_ok(events_call(1, empty_request, sizeof(empty_request) - 1,
                           response, sizeof(response)))) {
    return -4;
  }
  if (!call_ok(settings_call(1, empty_request, sizeof(empty_request) - 1,
                             response, sizeof(response)))) {
    return -5;
  }

  /* A response larger than resp_cap must be reported as the limit status,
   * without writing a partial envelope. */
  if (clipboard_call(1, empty_request, sizeof(empty_request) - 1,
                     response, 4) != -2) {
    return -6;
  }

  return host_output_write(message, sizeof(message) - 1) < 0 ? -7 : 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
