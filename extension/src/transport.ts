export type DeliveryKind = "delivered" | "retryable" | "permanent";

export interface DeliveryDecision {
  kind: DeliveryKind;
  code: string;
  error: string;
  status?: number;
  retryAfterMs?: number;
}

const permanentCodes = new Set([
  "event_id_conflict", "run_frozen", "contract_invalid", "identity_mismatch", "unknown_build",
]);

export function retryDelayMs(attempts: number): number {
  return Math.min(30_000 * (2 ** Math.max(0, attempts)), 32 * 60_000);
}

export function parseRetryAfter(value: string | null, now = Date.now()): number | undefined {
  if (!value) return undefined;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds > 0) return Math.min(seconds * 1000, 32 * 60_000);
  const date = Date.parse(value);
  if (!Number.isFinite(date) || date <= now) return undefined;
  return Math.min(date - now, 32 * 60_000);
}

export function classifyResponse(status: number, code = "http_error", error = "request failed", retryAfterMs?: number): DeliveryDecision {
  if (status === 200 || status === 202) return { kind: "delivered", code: "delivered", error: "delivered", status };
  if (status === 408 || status === 429 || status >= 500) {
    return {
      kind: "retryable", code, error, status,
      retryAfterMs,
    };
  }
  if (permanentCodes.has(code) || status >= 400 && status < 500) return { kind: "permanent", code, error, status };
  return { kind: "retryable", code, error, status };
}
