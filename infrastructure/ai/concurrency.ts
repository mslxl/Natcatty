/**
 * Run an array of async task factories with a concurrency limit.
 */
export async function limitConcurrency<T>(tasks: (() => Promise<T>)[], limit: number): Promise<T[]> {
  const results: T[] = [];
  const executing = new Set<Promise<void>>();
  for (const [i, task] of tasks.entries()) {
    const p: Promise<void> = task().then(r => { results[i] = r; }).finally(() => executing.delete(p));
    executing.add(p);
    if (executing.size >= limit) {
      await Promise.race(executing);
    }
  }
  await Promise.all(executing);
  return results;
}
