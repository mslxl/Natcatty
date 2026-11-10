/*
 * A module that never yields from dispatch, for deadline and one-in-flight
 * tests. It announces that the call started through the host output import,
 * then spins forever.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, int32_t len);

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }
EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }
/* Keep the command buffer away from the static "spin" constant. */
EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) { (void)size; return 65536; }
EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t p, uint32_t n) {
  static const uint8_t message[] = "spin";
  (void)p;
  (void)n;
  host_output_write(message, sizeof(message) - 1);
  for (;;) {
  }
  return 0;
}
EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
