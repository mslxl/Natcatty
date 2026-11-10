/*
 * The walking-skeleton guest. It exports the v1 lifecycle ABI, copies input
 * through natcatty_alloc, logs through natcatty_host_v1.log, and returns its
 * visible result through natcatty_host_v1.output_write.
 *
 * Built with clang --target=wasm32 and wasm-ld; see gen.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "log") extern void host_log(int32_t level, const uint8_t *ptr, int32_t len);
IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);

static uint8_t heap[65536];
static uint32_t used;

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  static const uint8_t message[] = "hello init";
  host_log(1, message, sizeof(message) - 1);
  return 0;
}

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  uint32_t ptr = (uint32_t)heap + used;
  used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  static const uint8_t message[] = "dispatch";
  static const uint8_t reply[] = "hello from plugin";
  (void)command_ptr;
  (void)command_len;
  host_log(1, message, sizeof(message) - 1);
  return host_output_write(reply, sizeof(reply) - 1) < 0 ? -1 : 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
