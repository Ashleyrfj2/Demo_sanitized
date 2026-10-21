#!/usr/bin/env node

import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const API = process.env.QA_API_URL || 'http://127.0.0.1:8080';
const OUTPUT = resolve('demo/.generated/synthetic-experiment.json');
const TOKENS = process.env.QA_API_TOKENS_JSON
  ? JSON.parse(process.env.QA_API_TOKENS_JSON)
  : JSON.parse(await readFile(resolve('demo/.generated/source-tokens.json'), 'utf8'));
const SCOPE = new URLSearchParams({
  run_id: 'festival-guided-001',
  tenant_id: 'festival-thesis-demo',
  build_id: 'festnest-demo-001',
  environment_id: 'festnest-local-browser',
});

if (!/^http:\/\/(127\.0\.0\.1|localhost)(:\d+)?$/.test(API)) {
  throw new Error('Refusing to send synthetic demo evidence to a non-loopback API');
}

async function request(path, options = {}, actorId = 'human:late-tester-d') {
  const token = TOKENS[actorId];
  if (!token) throw new Error(`QA_API_TOKENS_JSON is missing a token for ${actorId}`);
  const response = await fetch(`${API}${path}`, { ...options, headers: { authorization: `Bearer ${token}`, ...(options.headers || {}) } });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`${options.method || 'GET'} ${path}: ${response.status} ${JSON.stringify(body)}`);
  return body;
}

async function fixture(name) {
  return JSON.parse(await readFile(resolve(`testdata/trajectories/${name}.json`), 'utf8'));
}

async function postEvent(event) {
  const tokenKey = `${event.source_type}:${event.actor_id}`;
  return request('/api/v1/events', {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(event),
  }, tokenKey);
}

const receipts = {};
receipts.build_registration = await request('/api/v1/builds', {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({
    tenant_id: 'festival-thesis-demo',
    environment_id: 'festnest-local-browser',
    build_id: 'festnest-demo-001',
    sequence_number: 1,
  }),
}, 'controller:researcher-a');
for (const [id, mode] of [['festival-baseline-001', 'baseline'], ['festival-guided-001', 'guided']]) {
  receipts[`experiment_${mode}`] = await request('/api/v1/experiments', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ id, tenant_id: 'festival-thesis-demo', build_id: 'festnest-demo-001', environment_id: 'festnest-local-browser', scenario_id: 'equipment-handoff-v1', mode }),
  }, 'controller:researcher-a');
}
receipts.baseline = await postEvent(await fixture('sample-baseline'));
for (const name of ['sample-human', 'sample-adapter', 'sample-agent']) {
  receipts[name] = await postEvent(await fixture(name));
}

const recommendationQuery = new URLSearchParams(SCOPE);
recommendationQuery.set('actor_id', 'late-tester-d');
recommendationQuery.set('role', 'editor');
const recommendation = await request(`/api/v1/recommendations/next?${recommendationQuery}`, {}, 'human:late-tester-d');
receipts.recommendation_response = await request(`/api/v1/recommendations/${recommendation.id}/responses`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ response: 'accepted', reason: 'Highest-value unresolved stale-state check', actor_id: 'late-tester-d' }),
}, 'human:late-tester-d');
receipts.late_tester = await postEvent(await fixture('sample-late-tester'));
receipts.baseline_freeze = await request('/api/v1/experiments/festival-baseline-001/freeze', { method: 'POST' }, 'controller:researcher-a');
receipts.guided_freeze = await request('/api/v1/experiments/festival-guided-001/freeze', { method: 'POST' }, 'controller:researcher-a');

const artifact = {
  generated_at: new Date().toISOString(),
  synthetic: true,
  scope: Object.fromEntries(SCOPE),
  receipts,
  recommendation,
  evidence: await request(`/api/v1/evidence?${SCOPE}`, {}, 'human:late-tester-d'),
  baseline_metrics: await request('/api/v1/experiments/festival-baseline-001/metrics', {}, 'human:late-tester-d'),
  guided_metrics: await request('/api/v1/experiments/festival-guided-001/metrics', {}, 'human:late-tester-d'),
};
await mkdir(resolve('demo/.generated'), { recursive: true });
await writeFile(OUTPUT, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
console.log(`Synthetic experiment artifact written to ${OUTPUT}`);
