import express from "express";

import { jobsRouter } from "./api.js";
import { handlers } from "./handlers.js";
import { JobQueue } from "./queue.js";
import { defaultRetryPolicy } from "./retry.js";
import { JobStore } from "./store.js";
import { Worker } from "./worker.js";

const port = Number(process.env.PORT ?? 3000);

const store = new JobStore();
const queue = new JobQueue(store, handlers, { retry: defaultRetryPolicy, defaultMaxAttempts: 5 });
const worker = new Worker(queue, store, handlers, { concurrency: 4, pollMs: 200 });

const app = express();
app.use(express.json({ limit: "100kb" }));
app.use(jobsRouter(queue, store));

const server = app.listen(port, () => {
  console.log(`jobrunner listening on :${port}`);
});
worker.start();

let stopping = false;

async function shutdown(signal: string): Promise<void> {
  if (stopping) {
    return;
  }
  stopping = true;
  console.log(`${signal}: draining jobs`);
  server.close();
  await worker.drain();
  process.exit(0);
}

process.on("SIGTERM", () => void shutdown("SIGTERM"));
process.on("SIGINT", () => void shutdown("SIGINT"));
