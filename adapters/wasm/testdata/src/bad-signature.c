/*
 * A reactor with the right export and import names but the wrong wire
 * signatures: natcatty_poll takes an i32 and the log import omits the level.
 * Structural conformance must reject it from the IDL signatures alone.
 *
 * Built with clang --target=wasm32 and wasm-ld; see gen.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "log") extern void host_log(const uint8_t *ptr, int32_t len);

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  static const uint8_t message[] = "hello init";
  host_log(message, sizeof(message) - 1);
  return 0;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  (void)command_ptr;
  (void)command_len;
  return 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(int32_t status) { return status; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
