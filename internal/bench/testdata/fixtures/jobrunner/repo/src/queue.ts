import { randomUUID } from "node:crypto";
import type { Request } from "express";

import { backoff, type RetryPolicy } from "./retry.js";
import type { JobStore } from "./store.js";
import type { Handler, Job } from "./types.js";

export class ValidationError extends Error {}

export interface QueueOptions {
  retry: RetryPolicy;
  defaultMaxAttempts: number;
}

interface Pending {
  job: Job;
  readyAt: number;
}

/** Jobs waiting to run, each with the time it becomes ready. */
export class JobQueue {
  private pending: Pending[] = [];

  constructor(
    private readonly store: JobStore,
    private readonly handlers: ReadonlyMap<string, Handler>,
    private readonly options: QueueOptions,
  ) {}

  /** Validates a job submission, records it, and queues it to run now. */
  enqueue(req: Request): Job {
    const { type, payload, maxAttempts } = req.body ?? {};
    if (typeof type !== "string" || !this.handlers.has(type)) {
      throw new ValidationError(`unknown job type: ${String(type)}`);
    }
    if (maxAttempts !== undefined && (!Number.isInteger(maxAttempts) || maxAttempts < 1)) {
      throw new ValidationError("maxAttempts must be a positive integer");
    }
    const job: Job = {
      id: randomUUID(),
      type,
      payload,
      attempts: 0,
      maxAttempts: maxAttempts ?? this.options.defaultMaxAttempts,
    };
    const now = new Date().toISOString();
    this.store.put({
      id: job.id,
      type,
      status: "queued",
      attempts: 0,
      maxAttempts: job.maxAttempts,
      createdAt: now,
      updatedAt: now,
    });
    this.pending.push({ job, readyAt: Date.now() });
    return job;
  }

  /** Removes and returns up to `max` jobs that are ready at `now`. */
  takeReady(now: number, max: number): Job[] {
    const ready: Job[] = [];
    const waiting: Pending[] = [];
    for (const p of this.pending) {
      if (p.readyAt <= now && ready.length < max) {
        ready.push(p.job);
      } else {
        waiting.push(p);
      }
    }
    this.pending = waiting;
    return ready;
  }

  /** Puts a job back to run after `delayMs`. */
  requeue(job: Job, delayMs: number): void {
    this.pending.push({ job, readyAt: Date.now() + delayMs });
  }

  /** Delay before the next attempt of a job that has failed `attempts` times. */
  nextDelay(attempts: number): number {
    return backoff(attempts, this.options.retry);
  }

  get size(): number {
    return this.pending.length;
  }
}
