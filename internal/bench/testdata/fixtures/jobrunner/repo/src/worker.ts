import type { JobQueue } from "./queue.js";
import type { JobStore } from "./store.js";
import type { Handler, Job } from "./types.js";

export interface WorkerOptions {
  /** Most jobs running at once. */
  concurrency: number;
  /** How often to look for ready jobs. */
  pollMs: number;
}

/** Polls the queue and runs ready jobs, recording outcomes in the store. */
export class Worker {
  private timer: ReturnType<typeof setInterval> | undefined;
  private readonly active = new Set<Promise<void>>();

  constructor(
    private readonly queue: JobQueue,
    private readonly store: JobStore,
    private readonly handlers: ReadonlyMap<string, Handler>,
    private readonly options: WorkerOptions,
  ) {}

  start(): void {
    this.timer ??= setInterval(() => this.tick(), this.options.pollMs);
  }

  /**
   * Stops polling, runs every job that is ready now, and resolves once those
   * jobs and any already running have finished.
   */
  async drain(): Promise<void> {
    clearInterval(this.timer);
    this.timer = undefined;
    const jobs = this.queue.takeReady(Date.now(), Infinity);
    jobs.forEach(async (job) => {
      await this.run(job);
    });
    await Promise.all(this.active);
  }

  private tick(): void {
    const free = this.options.concurrency - this.active.size;
    if (free <= 0) {
      return;
    }
    for (const job of this.queue.takeReady(Date.now(), free)) {
      const p = this.run(job).finally(() => this.active.delete(p));
      this.active.add(p);
    }
  }

  private async run(job: Job): Promise<void> {
    this.store.update(job.id, { status: "running" });
    try {
      const handler = this.handlers.get(job.type);
      if (!handler) {
        throw new Error(`no handler for job type ${job.type}`);
      }
      const result = await handler(job.payload);
      job.attempts += 1;
      this.store.update(job.id, { status: "succeeded", attempts: job.attempts, result, error: undefined });
    } catch (err) {
      const error = err instanceof Error ? err.message : String(err);
      if (job.attempts >= job.maxAttempts) {
        this.store.update(job.id, { status: "failed", attempts: job.attempts, error });
        return;
      }
      this.store.update(job.id, { status: "queued", error });
      this.queue.requeue(job, this.queue.nextDelay(job.attempts));
    }
  }
}
