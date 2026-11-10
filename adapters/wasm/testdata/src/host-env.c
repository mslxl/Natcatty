/*
 * A module that exercises the host-controlled ambient values. It reads the
 * clock and fills an eight-byte buffer from random_get, then writes a stable
 * line through output_write. The fake host injects a virtual clock and a
 * deterministic reader, so the line is reproducible.
 */
typedef signed int int32_t;
typedef unsigned int uint32_t;
typedef unsigned char uint8_t;
typedef signed long long int64_t;
typedef unsigned long long uint64_t;

#define EXPORT(name) __attribute__((visibility("default"), export_name(name)))
#define IMPORT(module, name) __attribute__((import_module(module), import_name(name)))

IMPORT("natcatty_host_v1", "now_ns") extern int64_t host_now_ns(void);
IMPORT("natcatty_host_v1", "random_get") extern int32_t host_random_get(uint8_t *ptr, uint32_t len);
IMPORT("natcatty_host_v1", "output_write") extern int32_t host_output_write(const uint8_t *ptr, uint32_t len);

static uint32_t append_text(uint8_t *dst, uint32_t pos, const char *text) {
  while (*text != '\0') {
    dst[pos++] = (uint8_t)*text++;
  }
  return pos;
}

static uint32_t append_u64(uint8_t *dst, uint32_t pos, uint64_t value) {
  uint8_t digits[20];
  uint32_t count = 0;
  if (value == 0) {
    digits[count++] = '0';
  }
  while (value > 0) {
    digits[count++] = (uint8_t)('0' + (value % 10));
    value /= 10;
  }
  while (count > 0) {
    dst[pos++] = digits[--count];
  }
  return pos;
}

static uint32_t append_hex_byte(uint8_t *dst, uint32_t pos, uint8_t value) {
  static const char hex[] = "0123456789abcdef";
  dst[pos++] = (uint8_t)hex[value >> 4];
  dst[pos++] = (uint8_t)hex[value & 0xf];
  return pos;
}

EXPORT("natcatty_abi_version") int32_t natcatty_abi_version(void) { return 1; }

EXPORT("natcatty_init") int32_t natcatty_init(void) { return 0; }

EXPORT("natcatty_alloc") uint32_t natcatty_alloc(uint32_t size) {
  static uint8_t heap[4096];
  static uint32_t used;
  uint32_t ptr = (uint32_t)heap + used;
  used += (size + 7u) & ~7u;
  return ptr;
}

EXPORT("natcatty_dispatch") int32_t natcatty_dispatch(uint32_t command_ptr, uint32_t command_len) {
  uint8_t output[64];
  uint8_t random[8];
  uint32_t pos = 0;
  uint32_t i;
  (void)command_ptr;
  (void)command_len;
  if (host_random_get(random, sizeof(random)) != 0) {
    return -1;
  }
  pos = append_text(output, pos, "now=");
  pos = append_u64(output, pos, (uint64_t)host_now_ns());
  pos = append_text(output, pos, " rand=");
  for (i = 0; i < sizeof(random); i++) {
    pos = append_hex_byte(output, pos, random[i]);
  }
  return host_output_write(output, pos) < 0 ? -1 : 0;
}

EXPORT("natcatty_poll") int32_t natcatty_poll(void) { return 0; }

EXPORT("natcatty_shutdown") int32_t natcatty_shutdown(void) { return 0; }
