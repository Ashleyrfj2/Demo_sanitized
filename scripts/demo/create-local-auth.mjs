#!/usr/bin/env node

import { randomBytes } from 'node:crypto';
import { existsSync } from 'node:fs';
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const envPath = resolve(repoRoot, '.env');
const outputPath = resolve(repoRoot, 'demo/.generated/source-tokens.json');
const frontendEnvPath = resolve(repoRoot, 'frontend/.env.local');
if (existsSync(envPath) && !process.argv.includes('--force')) {
  throw new Error('.env already exists; preserve it or rerun with --force to replace only the local demo credentials');
}

const definitions = [
  ['human:baseline-human-a', 'festival-thesis-demo', 'baseline-human-a', 'human', 'chrome-mv3', 'editor', 'source'],
  ['human:editor-a', 'festival-thesis-demo', 'editor-a', 'human', 'chrome-mv3', 'editor', 'source'],
  ['automation:leader', 'festival-thesis-demo', 'leader', 'automation', 'festival-activity-log-v1', 'leader', 'source'],
  ['automation:editor-a', 'festival-thesis-demo', 'editor-a', 'automation', 'festival-activity-log-v1', 'editor', 'source'],
  ['automation:viewer-b', 'festival-thesis-demo', 'viewer-b', 'automation', 'festival-activity-log-v1', 'viewer', 'source'],
  ['automation:late-tester-d', 'festival-thesis-demo', 'late-tester-d', 'automation', 'festival-activity-log-v1', 'editor', 'source'],
  ['agent:playwright-agent-c', 'festival-thesis-demo', 'playwright-agent-c', 'agent', 'playwright', 'editor', 'source'],
  ['human:late-tester-d', 'festival-thesis-demo', 'late-tester-d', 'human', 'chrome-mv3', 'editor', 'source'],
  ['controller:researcher-a', 'festival-thesis-demo', 'researcher-a', 'human', 'controller', 'researcher', 'controller'],
];

const tokens = {};
const registry = definitions.map(([key, tenant, actor, source, adapter, role, capabilities]) => {
  const token = randomBytes(32).toString('base64url');
  tokens[key] = token;
  return [token, tenant, actor, source, adapter, role, capabilities].join('|');
});

const dbPassword = randomBytes(32).toString('base64url');
const env = [
  `DEMO_DB_PASSWORD=${dbPassword}`,
  'API_HOST=127.0.0.1',
  'API_PORT=8080',
  `QA_SOURCE_IDENTITIES=${registry.join(';')}`,
  'VITE_QA_API_BASE_URL=http://127.0.0.1:8080',
  '',
].join('\n');

await mkdir(dirname(outputPath), { recursive: true });
await writeFile(envPath, env, { mode: 0o600 });
await writeFile(frontendEnvPath, 'VITE_QA_API_BASE_URL=http://127.0.0.1:8080\n', { mode: 0o600 });
await writeFile(outputPath, `${JSON.stringify(tokens, null, 2)}\n`, { mode: 0o600 });
console.log('Created ignored local-only API credentials for the API and demo adapters. Enter the presenter token in the frontend at runtime.');
