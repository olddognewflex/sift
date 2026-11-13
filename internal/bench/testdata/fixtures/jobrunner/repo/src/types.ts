export type JobStatus = "queued" | "running" | "succeeded" | "failed";

/** A job as the queue and worker see it. */
export interface Job {
  id: string;
  type: string;
  payload: unknown;
  attempts: number;
  maxAttempts: number;
}

/** A job as stored for reporting. */
export interface JobRecord {
  id: string;
  type: string;
  status: JobStatus;
  attempts: number;
  maxAttempts: number;
  createdAt: string;
  updatedAt: string;
  result?: unknown;
  error?: string;
}

/** Runs one job; a rejection is a failed attempt. */
export type Handler = (payload: unknown) => Promise<unknown>;
