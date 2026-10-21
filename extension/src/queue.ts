import type { ValidationEvent } from "./normalize.js";

export const queueKey = "qaEventQueue";
export const statusKey = "transportStatus";
export const queueLimit = 100;

export interface QueuedEvent {
  event: ValidationEvent;
  attempts: number;
  state: "pending" | "permanent_failure";
  nextAttemptAt: string;
  lastErrorCode?: string;
  httpStatus?: number;
}

export interface TransportStatus {
  state: "disabled" | "ready" | "queued" | "retrying" | "blocked" | "error";
  pendingCount: number;
  failedCount: number;
  queueFullCount: number;
  updatedAt: string;
  lastSuccessAt?: string;
  lastFailure?: { code: string; error: string; status?: number; occurredAt: string };
}

export async function readQueue(): Promise<QueuedEvent[]> {
  const stored = await chrome.storage.local.get(queueKey);
  return Array.isArray(stored[queueKey]) ? stored[queueKey] : [];
}

export async function writeQueue(queue: QueuedEvent[]): Promise<void> {
  if (queue.length > queueLimit) throw new Error("queue_limit_exceeded");
  await chrome.storage.local.set({ [queueKey]: queue });
}

export async function readStatus(): Promise<TransportStatus> {
  const stored = await chrome.storage.local.get(statusKey);
  const value = stored[statusKey] as TransportStatus | undefined;
  return value ?? {
    state: "disabled", pendingCount: 0, failedCount: 0, queueFullCount: 0, updatedAt: new Date(0).toISOString(),
  };
}

export async function persistStatus(queue: QueuedEvent[], update: Partial<TransportStatus>): Promise<TransportStatus> {
  const previous = await readStatus();
  const status: TransportStatus = {
    ...previous, ...update,
    pendingCount: queue.filter((item) => item.state === "pending").length,
    failedCount: queue.filter((item) => item.state === "permanent_failure").length,
    updatedAt: new Date().toISOString(),
  };
  await chrome.storage.local.set({ [statusKey]: status });
  const failed = status.failedCount > 0 || status.state === "blocked" || status.state === "error";
  await chrome.action.setBadgeText({ text: failed ? "!" : status.pendingCount > 0 ? "…" : "" });
  await chrome.action.setBadgeBackgroundColor({ color: failed ? "#B42318" : "#B7791F" });
  return status;
}
