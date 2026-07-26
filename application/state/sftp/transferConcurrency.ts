export const DEFAULT_SFTP_FILE_TRANSFER_CONCURRENCY = 2;
export const MIN_SFTP_FILE_TRANSFER_CONCURRENCY = 1;
export const MAX_SFTP_FILE_TRANSFER_CONCURRENCY = 16;

export function resolveSftpTransferConcurrency(readStoredValue: () => number | null | undefined): number {
  const stored = readStoredValue();
  return stored != null &&
    stored >= MIN_SFTP_FILE_TRANSFER_CONCURRENCY &&
    stored <= MAX_SFTP_FILE_TRANSFER_CONCURRENCY
    ? stored
    : DEFAULT_SFTP_FILE_TRANSFER_CONCURRENCY;
}

export async function runSftpTransferWorkers<T>(
  items: T[],
  readStoredConcurrency: () => number | null | undefined,
  worker: (item: T, index: number) => Promise<void>,
  options?: {
    /**
     * Called before claiming the next queue index. Folder pause must wait here
     * so a worker that just finished soft-drain cannot claim the next file
     * while the parent is still latched (claim-before-wait started new work).
     */
    beforeClaim?: () => Promise<void>;
  },
): Promise<void> {
  const concurrency = resolveSftpTransferConcurrency(readStoredConcurrency);
  let nextIndex = 0;

  const runNext = async () => {
    while (nextIndex < items.length) {
      // Wait BEFORE claiming so pause does not leave a claimed-but-not-started
      // index that arms as soon as soft-drain finishes the previous file.
      if (options?.beforeClaim) {
        await options.beforeClaim();
      }
      if (nextIndex >= items.length) return;
      const index = nextIndex++;
      await worker(items[index], index);
    }
  };

  const workers = Array.from(
    { length: Math.min(concurrency, items.length) },
    () => runNext(),
  );
  await Promise.all(workers);
}
