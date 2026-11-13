# jobrunner

An in-process background job runner with an HTTP API. Jobs are submitted
over HTTP, run by a pool of workers, and retried with exponential backoff
until they succeed or run out of attempts.

```sh
npm install
npm run build
PORT=3000 npm start
```

| Method | Path | |
|---|---|---|
| `POST` | `/jobs` | Submit `{"type", "payload", "maxAttempts"?}`; returns `202 {"id"}` |
| `GET` | `/jobs?after=&limit=` | Jobs in id order, paged by cursor |
| `GET` | `/jobs/:id` | A job's status and attempt count |
| `GET` | `/jobs/:id/result` | A finished job's result (`204` if it returned nothing) |

Job types are registered in `src/handlers.ts`. On `SIGTERM` the server stops
accepting requests and drains the jobs that are ready before exiting.
