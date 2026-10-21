import assert from "node:assert/strict";
import test from "node:test";

function completeConfig() {
  return {
    tenantId: "festival-thesis-demo",
    buildId: "festnest-demo-001",
    environmentId: "festnest-local-browser",
    scenarioId: "equipment-handoff-v1",
    actorId: "editor-a",
    role: "editor",
    sessionId: "session-a",
    runId: "festival-guided-001",
    apiBaseUrl: "http://127.0.0.1:8080",
    apiToken: "test-only-token",
    captureEnabled: true,
  };
}

function envelope(control) {
  return {
    actionType: "control_activated",
    route: "/trip/supply-list",
    control,
    occurredAt: "2026-08-22T00:00:00.000Z",
    online: true,
    redactedFields: [],
  };
}

function createChrome(storage) {
  let messageListener;
  let alarmListener;
  return {
    api: {
      storage: { local: {
        async get(key) {
          if (typeof key === "string") return { [key]: storage[key] };
          return { ...storage };
        },
        async set(values) { Object.assign(storage, structuredClone(values)); },
      } },
      runtime: { onMessage: { addListener(listener) { messageListener = listener; } } },
      alarms: {
        onAlarm: { addListener(listener) { alarmListener = listener; } },
        async create() {},
        async clear() { return true; },
      },
      action: { async setBadgeText() {}, async setBadgeBackgroundColor() {} },
    },
    send(message) {
      return new Promise((resolve, reject) => {
        try {
          const asyncResponse = messageListener(message, {}, resolve);
          if (!asyncResponse) reject(new Error("listener did not keep the response channel open"));
        } catch (error) { reject(error); }
      });
    },
    alarm(name = "qa-evidence-retry") { alarmListener({ name }); },
  };
}

test("concurrent capture is serialized and queued event IDs survive retry and worker restart", async () => {
  const storage = { captureConfig: completeConfig() };
  const firstWorker = createChrome(storage);
  globalThis.chrome = firstWorker.api;
  const deliveredBodies = [];
  globalThis.fetch = async (_url, options) => {
    deliveredBodies.push(JSON.parse(options.body));
    return new Response(JSON.stringify({ code: "upstream_unavailable", error: "retry" }), {
      status: 503,
      headers: { "content-type": "application/json" },
    });
  };
  await import(`../dist/background.js?worker=first-${Date.now()}`);

  const responses = await Promise.all([
    firstWorker.send({ type: "QA_CAPTURE_ENVELOPE", payload: envelope("testid:pack") }),
    firstWorker.send({ type: "QA_CAPTURE_ENVELOPE", payload: envelope("testid:unpack") }),
  ]);
  assert.deepEqual(responses, [{ accepted: true }, { accepted: true }]);
  assert.equal(storage.qaEventQueue.length, 2);
  assert.deepEqual(storage.qaEventQueue.map((item) => item.event.step_index), [1, 2]);
  assert.equal(new Set(storage.qaEventQueue.map((item) => item.event.event_id)).size, 2);
  const queuedIds = storage.qaEventQueue.map((item) => item.event.event_id);

  for (const item of storage.qaEventQueue) item.nextAttemptAt = new Date(0).toISOString();
  const secondWorker = createChrome(storage);
  globalThis.chrome = secondWorker.api;
  globalThis.fetch = async (_url, options) => {
    deliveredBodies.push(JSON.parse(options.body));
    return new Response(null, { status: 202 });
  };
  await import(`../dist/background.js?worker=second-${Date.now()}`);
  await secondWorker.send({ type: "QA_GET_STATE" });

  assert.deepEqual(storage.qaEventQueue, []);
  assert.deepEqual(deliveredBodies.slice(-2).map((body) => body.event_id), queuedIds);
  assert.ok(deliveredBodies.every((body) => body.run_id === "festival-guided-001"));

  const rejected = await secondWorker.send({
    type: "QA_SAVE_CONFIG",
    payload: { captureEnabled: false, apiBaseUrl: "https://production.example.test" },
  });
  assert.deepEqual(rejected, { saved: false, errors: ["apiBaseUrl"] });
  const repairedState = await secondWorker.send({ type: "QA_GET_STATE" });
  assert.equal(repairedState.config.apiBaseUrl, "http://127.0.0.1:8080");
  assert.equal(repairedState.config.captureEnabled, true);
});

test("poisoned disabled storage is sanitized so state remains repairable", async () => {
  const storage = {
    captureConfig: { ...completeConfig(), captureEnabled: false, apiBaseUrl: "not a URL" },
  };
  const worker = createChrome(storage);
  globalThis.chrome = worker.api;
  globalThis.fetch = async () => { throw new Error("invalid destination must never be fetched"); };
  await import(`../dist/background.js?worker=repair-${Date.now()}`);
  const state = await worker.send({ type: "QA_GET_STATE" });
  assert.equal(state.config.apiBaseUrl, "http://127.0.0.1:8080");
  assert.equal(state.config.captureEnabled, false);
});
