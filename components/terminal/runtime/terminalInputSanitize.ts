/**
 * Strip zero-width / invisible Unicode formatting characters from terminal
 * input before it reaches the PTY.
 *
 * CJK IMEs (notably Microsoft Pinyin / Sogou on Windows) occasionally emit
 * zero-width characters when switching composition modes. xterm.js sends
 * these to the PTY via `onData`, and with the `15-graphemes` Unicode
 * version they render at width 0 — so the command line looks normal but
 * contains hidden characters that cause the executed command to fail (#3138).
 *
 * These characters are never legitimate in terminal input: the shell cannot
 * display or process them, and a remote PTY has no grapheme renderer.
 * Stripping them from the input path does not affect output rendering,
 * which goes through a separate write pipeline.
 */

// U+00AD  SOFT HYPHEN
// U+200B  ZERO WIDTH SPACE
// U+200C  ZERO WIDTH NON-JOINER
// U+200D  ZERO WIDTH JOINER  (also used inside emoji sequences; in terminal
//                           input the shell receives raw bytes and cannot
//                           benefit from a joiner, so stripping is safe)
// U+200E  LEFT-TO-RIGHT MARK
// U+200F  RIGHT-TO-LEFT MARK
// U+2060  WORD JOINER
// U+2061  FUNCTION APPLICATION
// U+2062  INVISIBLE TIMES
// U+2063  INVISIBLE SEPARATOR
// U+2064  INVISIBLE PLUS
// U+FEFF  ZERO WIDTH NO-BREAK SPACE / BOM
const ZERO_WIDTH_INPUT_RE = /[\u00ad\u200b-\u200f\u2060-\u2064\ufeff]/g;

/**
 * Remove zero-width / invisible formatting characters from terminal input.
 * Returns an empty string when the input consisted solely of such characters.
 */
export function sanitizeTerminalInput(data: string): string {
  if (!data) return data;
  return data.replace(ZERO_WIDTH_INPUT_RE, "");
}
