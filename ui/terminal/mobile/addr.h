//go:build ios || android

#ifndef NATCATTY_GHOSTTY_ADDR_H
#define NATCATTY_GHOSTTY_ADDR_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* natcatty_ghostty_addr returns the address of a ghostty_* symbol linked
 * into the app, or 0 when the library does not export it. */
uintptr_t natcatty_ghostty_addr(const char *name);

#ifdef __cplusplus
}
#endif

#endif
