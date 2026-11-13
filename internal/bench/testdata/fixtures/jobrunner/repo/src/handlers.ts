import type { Handler } from "./types.js";

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Sums a list of numbers; stands in for a slow reporting query. */
const report: Handler = async (payload) => {
  const rows = (payload as { rows?: unknown } | null)?.rows;
  if (!Array.isArray(rows) || !rows.every((n) => typeof n === "number")) {
    throw new Error("report: payload.rows must be an array of numbers");
  }
  await sleep(50);
  const total = rows.reduce((sum: number, n: number) => sum + n, 0);
  return { count: rows.length, total, mean: rows.length > 0 ? total / rows.length : 0 };
};

/** Waits for the requested time and returns nothing. */
const pause: Handler = async (payload) => {
  const ms = (payload as { ms?: unknown } | null)?.ms;
  if (typeof ms !== "number" || ms < 0 || ms > 60_000) {
    throw new Error("pause: payload.ms must be a number from 0 to 60000");
  }
  await sleep(ms);
};

/** The job types this runner accepts, by name. */
export const handlers: ReadonlyMap<string, Handler> = new Map([
  ["report", report],
  ["pause", pause],
]);
