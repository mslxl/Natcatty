/**
 * Anthropic-compatible gateways disagree on Base URL shape:
 * - Claude Code / official host: bare origin (…/host), paths are /v1/messages, /v1/models
 * - @ai-sdk/anthropic: base already includes /v1, then appends /messages, /models
 *
 * Netcatty accepts either user input and normalizes at the chat / probe boundaries.
 */

/** Strip trailing slashes; empty input stays empty. */
export function stripTrailingSlashes(url: string): string {
  return url.trim().replace(/\/+$/, "");
}

/** True when the URL path already ends with /v1 (AI SDK style). */
export function anthropicBaseIncludesV1(baseURL: string): boolean {
  return /\/v1$/i.test(stripTrailingSlashes(baseURL));
}

/**
 * Normalize a stored Anthropic-compat Base URL for @ai-sdk/anthropic.
 * Bare hosts gain a /v1 suffix; AI SDK style bases are left unchanged.
 */
export function normalizeAnthropicSdkBaseURL(baseURL: string): string {
  const trimmed = stripTrailingSlashes(baseURL);
  if (!trimmed) return trimmed;
  if (anthropicBaseIncludesV1(trimmed)) return trimmed;
  return `${trimmed}/v1`;
}
