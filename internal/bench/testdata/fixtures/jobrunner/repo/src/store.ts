import type { JobRecord } from "./types.js";

/** In-memory job records. Returned records are copies. */
export class JobStore {
  private readonly records = new Map<string, JobRecord>();

  put(record: JobRecord): void {
    this.records.set(record.id, { ...record });
  }

  get(id: string): JobRecord | undefined {
    const record = this.records.get(id);
    return record && { ...record };
  }

  update(id: string, changes: Partial<Omit<JobRecord, "id" | "createdAt">>): JobRecord {
    const current = this.records.get(id);
    if (!current) {
      throw new Error(`unknown job ${id}`);
    }
    const next = { ...current, ...changes, updatedAt: new Date().toISOString() };
    this.records.set(id, next);
    return { ...next };
  }

  /**
   * Up to `limit` records whose ids sort after `after`, in id order. Paging
   * by id keeps pages stable while new jobs arrive.
   */
  page(after: string | undefined, limit: number): JobRecord[] {
    const ids = [...this.records.keys()].filter((id) => after === undefined || id > after);
    ids.sort();
    return ids.slice(0, limit).map((id) => this.get(id)!);
  }
}
