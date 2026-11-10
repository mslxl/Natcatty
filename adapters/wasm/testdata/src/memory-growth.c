/*
 * A module that grows its linear memory until the declared maximum, then
 * stores one byte past the end. The host must contain the resulting trap and
 * keep the memory below the maximum.
 *
 * This module is linked with a 1 MiB maximum; gen.sh special-cases it.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }
EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }
EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) { (void)size; return 1024; }
EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t p, uint32_t n) {
  (void)p;
  (void)n;
  /* 64 KiB pages: grow until the module maximum rejects the request. */
  while (__builtin_wasm_memory_grow(0, 1) != -1) {
  }
  /* 0x100000 is the first byte past a 1 MiB memory. */
  *(volatile uint8_t *)0x100000u = 1;
  return 0;
}
EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }
EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
