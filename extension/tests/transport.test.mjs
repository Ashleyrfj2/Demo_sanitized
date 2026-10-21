import assert from 'node:assert/strict';
import test from 'node:test';
import { classifyResponse, parseRetryAfter, retryDelayMs } from '../dist/transport.js';
import { normalize } from '../dist/normalize.js';

test('transport responses are classified without retrying permanent failures', () => {
  assert.equal(classifyResponse(202).kind, 'delivered');
  assert.equal(classifyResponse(429).kind, 'retryable');
  assert.deepEqual(classifyResponse(409, 'run_frozen', 'run cannot accept writes'), {
    kind: 'permanent', code: 'run_frozen', error: 'run cannot accept writes', status: 409,
  });
  assert.equal(classifyResponse(422, 'contract_invalid', 'invalid event').kind, 'permanent');
  assert.equal(retryDelayMs(10), 32 * 60_000);
  assert.equal(parseRetryAfter('60', 0), 60_000);
  assert.equal(parseRetryAfter(new Date(90_000).toUTCString(), 30_000), 60_000);
});

test('passive activation normalizes as ambiguous and carries run scope', () => {
  const event = normalize({ actionType: 'control_activated', route: '/trip/supply-list', control: 'testid:pack', occurredAt: '2026-08-22T00:00:00Z', online: true, redactedFields: [] }, {
    tenantId: 'festival-thesis-demo', buildId: 'festnest-demo-001', environmentId: 'festnest-local-browser', scenarioId: 'equipment-handoff-v1', actorId: 'editor-a', role: 'editor', sessionId: 'session-a', runId: 'festival-guided-001', apiBaseUrl: 'http://127.0.0.1:8080', apiToken: 'not-rendered', captureEnabled: true,
  }, 7);
  assert.equal(event.outcome, 'ambiguous');
  assert.equal(event.run_id, 'festival-guided-001');
  assert.equal(event.step_index, 7);
});
