const REDACTED = '[REDACTED]';

const PRIVATE_KEY_PATTERN = /-----BEGIN ([A-Z0-9 ]*PRIVATE KEY)-----[\s\S]*?-----END \1-----/g;
const BEARER_PATTERN = /\b(Bearer)\s+[A-Za-z0-9._~+/=-]{8,}/gi;
const URL_CREDENTIAL_PATTERN = /([a-z][a-z0-9+.-]*:\/\/[^\s:/@]+:)([^\s@]+)(@)/gi;
const SECRET_ASSIGNMENT_PATTERN = /\b([A-Za-z0-9_.-]*(?:password|passwd|pwd|token|secret|api[_-]?key|access[_-]?key)[A-Za-z0-9_.-]*)\s*([:=])\s*(["']?)([^\s"',;]+)\3/gi;
const SECRET_CLI_FLAG_PATTERN = /(--(?:password|passwd|token|secret|api-key|access-key))\s+(?:["']([^"']+)["']|([^\s]+))/gi;
const WELL_KNOWN_TOKEN_PATTERN = /\b(?:gh[pousr]_[A-Za-z0-9]{20,}|sk-[A-Za-z0-9_-]{20,})\b/g;

/** Redacts likely credentials only on data that is about to become model-visible. */
export function redactSecretsForModel(value: string): string {
  if (!value) return value;
  return value
    .replace(PRIVATE_KEY_PATTERN, REDACTED)
    .replace(BEARER_PATTERN, '$1 [REDACTED]')
    .replace(URL_CREDENTIAL_PATTERN, `$1${REDACTED}$3`)
    .replace(SECRET_ASSIGNMENT_PATTERN, '$1$2[REDACTED]')
    .replace(SECRET_CLI_FLAG_PATTERN, '$1 [REDACTED]')
    .replace(WELL_KNOWN_TOKEN_PATTERN, REDACTED);
}

/** Recursively redacts model-visible event arguments without mutating the source value. */
export function redactSecretsInValueForModel<T>(value: T): T {
  if (typeof value === 'string') return redactSecretsForModel(value) as T;
  if (Array.isArray(value)) return value.map(redactSecretsInValueForModel) as T;
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>)
      .map(([key, entry]) => [key, redactSecretsInValueForModel(entry)]),
  ) as T;
}
