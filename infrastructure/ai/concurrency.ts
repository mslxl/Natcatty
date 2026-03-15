/**
 * Run an array of async task factories with a concurrency limit.
 */
export async function limitConcurrency<T>(tasks: (() => Promise<T>)[], limit: number): Promise<T[]> {
  const results: T[] = [];
  const errors: Array<{ index: number; error: unknown }> = [];
  const executing = new Set<Promise<void>>();
  for (const [i, task] of tasks.entries()) {
    const p: Promise<void> = task()
      .then(r => { results[i] = r; })
      .catch(err => { errors.push({ index: i, error: err }); })
      .finally(() => executing.delete(p));
    executing.add(p);
    if (executing.size >= limit) {
      await Promise.race(executing);
    }
  }
  await Promise.all(executing);
  if (errors.length > 0) {
    const msgs = errors.map(e => `Task ${e.index}: ${e.error instanceof Error ? e.error.message : String(e.error)}`);
    throw new AggregateError(errors.map(e => e.error), `${errors.length} task(s) failed: ${msgs.join('; ')}`);
  }
  return results;
}
