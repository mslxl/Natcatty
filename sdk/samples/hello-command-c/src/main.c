/*
 * C command sample built against the generated C bindings.
 *
 * natcatty_v1.h and natcatty_v1.c are generated from sdk/idl/natcatty_v1.idl
 * and checked in beside this file, so the sample has no include path outside
 * its own directory. natcatty_guest_init (generated) performs the init-time ABI
 * negotiation before the sample reads a command.
 *
 * Rebuild with ./build.sh; the committed module and plugin.toml hash must
 * always be regenerated together.
 */
#include "natcatty_v1.h"

static const char hello[] = "hello from c";

/* The host writes a command into this fixed scratch buffer because the sample
 * has no allocator. */
static unsigned char scratch[4096];

NATCATTY_EXPORT_ABI_VERSION natcatty_i32 natcatty_abi_version(void) {
  return NATCATTY_ABI_VERSION;
}

NATCATTY_EXPORT_INIT natcatty_i32 natcatty_init(void) {
  return natcatty_guest_init();
}

NATCATTY_EXPORT_DISPATCH natcatty_i32 natcatty_dispatch(natcatty_u32 command_ptr,
                                                        natcatty_u32 command_len) {
  (void)command_ptr;
  (void)command_len;
  if (natcatty_host_output_write((natcatty_u32)(unsigned long)hello,
                                 (natcatty_u32)(sizeof(hello) - 1)) < 0) {
    return -1;
  }
  return 0;
}

NATCATTY_EXPORT_SCRATCH natcatty_u32 natcatty_scratch(void) {
  return NATCATTY_PTR(scratch);
}

NATCATTY_EXPORT_POLL natcatty_i32 natcatty_poll(void) { return 0; }

NATCATTY_EXPORT_SHUTDOWN natcatty_i32 natcatty_shutdown(void) { return 0; }
