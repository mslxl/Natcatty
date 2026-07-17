import { PACKAGE_LIMITS } from "./constants.js";

const WINDOWS_RESERVED_NAME = /^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$/i;
const WINDOWS_SPECIAL = /[<>:"|?*]/;

function containsControlCharacter(value: string): boolean {
  return [...value].some((character) => character.charCodeAt(0) <= 0x1f);
}

export function assertSafePackagePath(input: string): string {
  if (!input || input !== input.normalize("NFC")) {
    throw new Error(`Package path must be non-empty NFC text: ${JSON.stringify(input)}`);
  }
  if ([...input].length > PACKAGE_LIMITS.pathCharacters) {
    throw new Error(
      `Package path exceeds ${PACKAGE_LIMITS.pathCharacters} Unicode characters: ${input}`,
    );
  }
  if (Buffer.byteLength(input, "utf8") > PACKAGE_LIMITS.pathBytes) {
    throw new Error(`Package path exceeds ${PACKAGE_LIMITS.pathBytes} UTF-8 bytes: ${input}`);
  }
  if (input.startsWith("/") || /^[A-Za-z]:/.test(input) || input.includes("\\")) {
    throw new Error(`Package path must be relative POSIX syntax: ${input}`);
  }
  const segments = input.split("/");
  if (segments.some((segment) => segment === "" || segment === "." || segment === "..")) {
    throw new Error(`Package path contains an unsafe segment: ${input}`);
  }
  for (const segment of segments) {
    if (
      segment.endsWith(".")
      || segment.endsWith(" ")
      || WINDOWS_RESERVED_NAME.test(segment)
      || WINDOWS_SPECIAL.test(segment)
      || containsControlCharacter(segment)
    ) {
      throw new Error(`Package path is not portable across supported platforms: ${input}`);
    }
  }
  return input;
}

export class PackagePathRegistry {
  readonly #exact = new Set<string>();
  readonly #portable = new Set<string>();

  add(input: string): string {
    const safePath = assertSafePackagePath(input);
    const portableKey = safePath.toLowerCase();
    if (this.#exact.has(safePath) || this.#portable.has(portableKey)) {
      throw new Error(`Duplicate or case-colliding package path: ${safePath}`);
    }
    this.#exact.add(safePath);
    this.#portable.add(portableKey);
    return safePath;
  }
}
