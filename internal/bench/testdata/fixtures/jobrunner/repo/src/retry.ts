export interface RetryPolicy {
  /** Delay before the first retry. */
  baseMs: number;
  /** Upper bound on any delay. */
  maxMs: number;
}

export const defaultRetryPolicy: RetryPolicy = { baseMs: 500, maxMs: 60_000 };

/**
 * Delay before retrying a job that has failed `attempts` times: doubling
 * from `baseMs`, capped at `maxMs`, less up to 20% jitter so that jobs
 * which failed together do not retry together.
 */
export function backoff(attempts: number, policy: RetryPolicy, random: () => number = Math.random): number {
  const delay = Math.min(policy.maxMs, policy.baseMs * 2 ** Math.max(0, attempts - 1));
  return Math.round(delay - delay * 0.2 * random());
}
