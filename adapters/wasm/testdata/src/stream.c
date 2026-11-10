/*
 * A guest that exercises the connection-provider stream imports. It reads one
 * input chunk, samples both credit counters, and echoes the chunk through
 * provider_output_write. Echoing makes the host observable: the contract suite
 * supplies the input and checks the same bytes arrive on the output callback.
 *
 * Built with clang --target=wasm32 and wasm-ld; see gen.sh.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "provider_input_read")
extern int32_t host_input_read(uint32_t stream, uint8_t *ptr, uint32_t capacity);
IMPORT("natcatty_host_v1", "provider_input_credit")
extern int32_t host_input_credit(uint32_t stream);
IMPORT("natcatty_host_v1", "provider_output_write")
extern int32_t host_output_write(uint32_t stream, const uint8_t *ptr, uint32_t len);
IMPORT("natcatty_host_v1", "provider_output_credit")
extern int32_t host_output_credit(uint32_t stream);

static uint8_t buffer[64];

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  return (uint32_t)buffer + (size & 0u);
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr,
                                                     uint32_t command_len) {
  (void)command_ptr;
  (void)command_len;
  int32_t count = host_input_read(7, buffer, sizeof(buffer));
  if (count < 0) {
    return -1;
  }
  if (host_input_credit(7) < 0) {
    return -2;
  }
  if (host_output_credit(7) < 0) {
    return -3;
  }
  if (host_output_write(7, buffer, (uint32_t)count) != 0) {
    return -4;
  }
  return 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
