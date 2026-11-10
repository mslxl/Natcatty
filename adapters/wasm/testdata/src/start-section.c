/*
 * A valid v1 reactor whose core module also declares a start section. The
 * start function is not exported as _start, so the export checks alone cannot
 * see it, yet wazero would run it during InstantiateModule, before
 * natcatty_init and outside every plugin deadline.
 *
 * The start function logs through the host import so a test that fails to
 * reject the module can observe that guest code ran. wasm-ld only emits a
 * start section for PIC or shared-memory output, so gen.sh links this reactor
 * with plugin_start exported and then rewrites the module to remove that
 * export and insert the core start section.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "log") extern void host_log(int32_t level, const uint8_t *ptr, int32_t len);

void plugin_start(void) {
  static const uint8_t message[] = "start section ran";
  host_log(1, message, sizeof(message) - 1);
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }
EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }
EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  (void)command_ptr;
  (void)command_len;
  return 0;
}
EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
