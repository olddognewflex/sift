import express, { type Request, type Response } from "express";

import { ValidationError, type JobQueue } from "./queue.js";
import type { JobStore } from "./store.js";
import type { JobStatus } from "./types.js";

const DEFAULT_PAGE = 20;
const MAX_PAGE = 100;

export function jobsRouter(queue: JobQueue, store: JobStore) {
  const router = express.Router();
  const statusCache = new Map<string, JobStatus>();

  router.get("/jobs/:id", (req: Request, res: Response) => {
    const record = store.get(req.params.id);
    if (record === undefined) {
      res.status(404).json({ error: "job not found" });
      return;
    }
    const status = statusCache.get(record.id) ?? record.status;
    statusCache.set(record.id, status);
    res.json({ id: record.id, type: record.type, status, attempts: record.attempts, error: record.error });
  });

  router.get("/jobs", (req: Request, res: Response) => {
    const after = typeof req.query.after === "string" ? req.query.after : undefined;
    const n = Number(req.query.limit);
    const limit = Number.isInteger(n) && n > 0 ? Math.min(n, MAX_PAGE) : DEFAULT_PAGE;
    const jobs = store.page(after, limit);
    // Results can be large; the list leaves them out.
    for (const job of jobs) {
      delete job.result;
    }
    res.json({ jobs, next: jobs.length === limit ? jobs[jobs.length - 1].id : null });
  });

  router.post("/jobs", (req: Request, res: Response) => {
    try {
      const job = queue.enqueue(req);
      res.status(202).json({ id: job.id, status: "queued" });
    } catch (err) {
      if (err instanceof ValidationError) {
        res.status(400).json({ error: err.message });
        return;
      }
      throw err;
    }
  });

  router.get("/jobs/:id/result", (req: Request, res: Response) => {
    const job = store.get(req.params.id);
    if (job === undefined) {
      res.status(404).json({ error: "job not found" });
      return;
    }
    if (job.status !== "succeeded") {
      res.status(409).json({ error: `job is ${job.status}` });
      return;
    }
    if (job.result == null) {
      res.status(204).end();
      return;
    }
    res.json({ result: job.result });
  });

  return router;
}
