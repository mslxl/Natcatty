export type CattyRequestTooLargeRetryError = Error & {
  cattyHadToolProgress?: boolean;
  statusCode?: number;
  status?: number;
  responseBody?: string;
};

export function createCattyRequestTooLargeRetryError(
  error: unknown,
  hadToolProgress: boolean,
): CattyRequestTooLargeRetryError {
  const message = error instanceof Error
    ? error.message
    : String(error ?? 'Request too large');
  const retryError = new Error(message) as CattyRequestTooLargeRetryError;
  retryError.name = 'CattyRequestTooLargeRetryError';
  retryError.cause = error;
  retryError.cattyHadToolProgress = hadToolProgress;
  retryError.statusCode = 413;
  if (error && typeof error === 'object') {
    const source = error as Record<string, unknown>;
    if (typeof source.status === 'number') retryError.status = source.status;
    if (typeof source.responseBody === 'string') retryError.responseBody = source.responseBody;
  }
  return retryError;
}

export function hadToolProgressBeforeRequestTooLarge(error: unknown): boolean {
  return !!(
    error &&
    typeof error === 'object' &&
    (error as { cattyHadToolProgress?: boolean }).cattyHadToolProgress
  );
}
