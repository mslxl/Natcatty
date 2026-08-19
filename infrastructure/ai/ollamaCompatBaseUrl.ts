/**
 * Ollama Cloud uses the OpenAI-compat host https://ollama.com/v1.
 * A bare https://ollama.com origin hits the marketing homepage (HTML) instead
 * of /v1/chat/completions. Local Ollama stays on localhost and is unchanged.
 */

/** Append /v1 when the user entered the Cloud origin without an API prefix. */
export function normalizeOllamaSdkBaseURL(baseURL: string): string {
  const trimmed = baseURL.trim().replace(/\/+$/, '');
  if (/^https?:\/\/ollama\.com$/i.test(trimmed)) {
    return `${trimmed}/v1`;
  }
  return trimmed || baseURL;
}
