/*
 * A module that imports WASI preview1. The v1 host allows no WASI imports at
 * all, so structural conformance must reject it without executing it.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("wasi_snapshot_preview1", "fd_write")
extern int32_t wasi_fd_write(int32_t fd, uint32_t iovs, uint32_t iovs_len, uint32_t nwritten);

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) {
  return wasi_fd_write(1, 0, 0, 0) == 0 ? 0 : -1;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t p, uint32_t n) {
  (void)p;
  (void)n;
  return 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
