/* A module that declares an unsupported wire ABI. */
typedef signed int int32_t;
typedef unsigned int uint32_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 2; }
EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }
EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t p, uint32_t n) {
  (void)p;
  (void)n;
  return 0;
}
EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
