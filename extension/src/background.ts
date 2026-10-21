import { configIsComplete, identityConfigFields, loadConfig, validateConfig, type CaptureConfig } from "./config.js";
import { normalize, type CaptureEnvelope } from "./normalize.js";
import { persistStatus, queueLimit, readQueue, readStatus, writeQueue, type QueuedEvent } from "./queue.js";
import { classifyResponse, parseRetryAfter, retryDelayMs, type DeliveryDecision } from "./transport.js";

const alarmName = "qa-evidence-retry";
const stepKey = "qaStepIndex";
let operation: Promise<void> = Promise.resolve();

function serial<T>(work: () => Promise<T>): Promise<T> {
  const result = operation.then(work, work);
  operation = result.then(() => undefined, () => undefined);
  return result;
}

async function nextStepIndex(): Promise<number> {
  const stored = await chrome.storage.local.get(stepKey);
  const next = Number(stored[stepKey] ?? 0) + 1;
  await chrome.storage.local.set({ [stepKey]: next });
  return next;
}

async function deliver(item: QueuedEvent): Promise<DeliveryDecision> {
  const config = await loadConfig();
  try {
    const response = await fetch(`${config.apiBaseUrl}/api/v1/events`, {
      method: "POST",
      headers: { authorization: `Bearer ${config.apiToken}`, "content-type": "application/json" },
      body: JSON.stringify(item.event),
    });
    let code = "http_error";
    let error = `HTTP ${response.status}`;
    try {
      const body = await response.clone().json() as { code?: string; error?: string };
      if (body.code) code = body.code;
      if (body.error) error = body.error;
    } catch { /* Status remains sufficient for classification. */ }
    return classifyResponse(response.status, code, error, parseRetryAfter(response.headers.get("Retry-After")));
  } catch {
    return { kind: "retryable", code: "network_error", error: "network request failed" };
  }
}

async function schedule(queue: QueuedEvent[]): Promise<void> {
  const pending = queue.filter((item) => item.state === "pending");
  if (pending.length === 0) { await chrome.alarms.clear(alarmName); return; }
  const earliest = Math.min(...pending.map((item) => Date.parse(item.nextAttemptAt)));
  await chrome.alarms.create(alarmName, { when: Math.max(Date.now() + 500, earliest) });
}

async function flushQueue(): Promise<void> {
  const queue = await readQueue();
  const now = Date.now();
  let lastSuccessAt: string | undefined;
  let lastFailure: { code: string; error: string; status?: number; occurredAt: string } | undefined;
  const remaining: QueuedEvent[] = [];
  for (const item of queue) {
    if (item.state === "permanent_failure" || Date.parse(item.nextAttemptAt) > now) { remaining.push(item); continue; }
    const decision = await deliver(item);
    if (decision.kind === "delivered") { lastSuccessAt = new Date().toISOString(); continue; }
    const attempts = item.attempts + 1;
    lastFailure = { code: decision.code, error: decision.error, status: decision.status, occurredAt: new Date().toISOString() };
    if (decision.kind === "permanent") {
      remaining.push({ ...item, attempts, state: "permanent_failure", lastErrorCode: decision.code, httpStatus: decision.status });
    } else {
      remaining.push({
        ...item, attempts, lastErrorCode: decision.code, httpStatus: decision.status,
        nextAttemptAt: new Date(Date.now() + (decision.retryAfterMs ?? retryDelayMs(attempts - 1))).toISOString(),
      });
    }
  }
  await writeQueue(remaining);
  const failed = remaining.some((item) => item.state === "permanent_failure");
  const pending = remaining.some((item) => item.state === "pending");
  const config = await loadConfig();
  await persistStatus(remaining, {
    state: failed ? "error" : pending ? "queued" : config.captureEnabled ? "ready" : "disabled",
    ...(lastSuccessAt ? { lastSuccessAt } : {}), ...(lastFailure ? { lastFailure } : {}),
  });
  await schedule(remaining);
}

async function enqueue(envelope: CaptureEnvelope): Promise<{ accepted: boolean; code?: string }> {
  const config = await loadConfig();
  if (!configIsComplete(config)) return { accepted: false, code: "capture_disabled_or_incomplete" };
  const queue = await readQueue();
  if (queue.length >= queueLimit) {
    const status = await readStatus();
    await persistStatus(queue, { state: "blocked", queueFullCount: status.queueFullCount + 1,
      lastFailure: { code: "queue_full", error: "capture queue is full", occurredAt: new Date().toISOString() } });
    return { accepted: false, code: "queue_full" };
  }
  const event = normalize(envelope, config, await nextStepIndex());
  queue.push({ event, attempts: 0, state: "pending", nextAttemptAt: new Date().toISOString() });
  await writeQueue(queue);
  await persistStatus(queue, { state: "queued" });
  await flushQueue();
  return { accepted: true };
}

async function saveConfig(patch: Partial<CaptureConfig>): Promise<{ saved: boolean; errors?: string[]; code?: string }> {
  const current = await loadConfig();
  const queue = await readQueue();
  const candidate = { ...current, ...patch, apiToken: patch.apiToken?.trim() || current.apiToken };
  if (queue.length > 0 && identityConfigFields.some((field) => candidate[field] !== current[field])) {
    return { saved: false, code: "identity_locked_while_queue_nonempty" };
  }
  const errors = validateConfig(candidate);
  const blockingErrors = candidate.captureEnabled ? errors : errors.filter((error) => error === "apiBaseUrl");
  if (blockingErrors.length > 0) return { saved: false, errors: blockingErrors };
  await chrome.storage.local.set({ captureConfig: candidate });
  await persistStatus(queue, { state: candidate.captureEnabled ? "ready" : "disabled" });
  return { saved: true };
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  void serial(async () => {
    if (message?.type === "QA_CAPTURE_ENVELOPE") return enqueue(message.payload as CaptureEnvelope);
    if (message?.type === "QA_GET_STATE") {
      const config = await loadConfig();
      const { apiToken: _secret, ...safeConfig } = config;
      return { config: safeConfig, status: await readStatus(), queue: await readQueue() };
    }
    if (message?.type === "QA_SAVE_CONFIG") return saveConfig(message.payload as Partial<CaptureConfig>);
    if (message?.type === "QA_RETRY_FAILED") {
      const queue = (await readQueue()).map((item) => item.state === "permanent_failure"
        ? { ...item, state: "pending" as const, nextAttemptAt: new Date().toISOString() } : item);
      await writeQueue(queue); await flushQueue(); return { retried: true };
    }
    return { accepted: false, code: "unsupported_message" };
  }).then(sendResponse).catch(async (error) => {
    const queue = await readQueue();
    await persistStatus(queue, { state: "error", lastFailure: { code: "capture_failed", error: "capture operation failed", occurredAt: new Date().toISOString() } });
    sendResponse({ accepted: false, code: error instanceof Error ? error.message : "capture_failed" });
  });
  return true;
});

chrome.alarms.onAlarm.addListener((alarm) => { if (alarm.name === alarmName) void serial(flushQueue); });
void serial(flushQueue);
