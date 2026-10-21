import assert from 'node:assert/strict';
import test from 'node:test';

const storage = {};
globalThis.chrome = {
  storage: { local: {
    async get(key) { return typeof key === 'string' ? { [key]: storage[key] } : { ...storage }; },
    async set(values) { Object.assign(storage, values); },
  } },
  action: { async setBadgeText() {}, async setBadgeBackgroundColor() {} },
};

const { queueKey, queueLimit, readQueue, writeQueue } = await import('../dist/queue.js');

function item(index) {
  return {
    event: { event_id: `event-${index}` }, attempts: 0, state: 'pending',
    nextAttemptAt: '2026-08-22T00:00:00.000Z',
  };
}

test('bounded queue rejects overflow without truncating durable events', async () => {
  const original = Array.from({ length: queueLimit }, (_, index) => item(index));
  await writeQueue(original);
  await assert.rejects(writeQueue([...original, item(queueLimit)]), /queue_limit_exceeded/);
  assert.equal((await readQueue()).length, queueLimit);
  assert.deepEqual(storage[queueKey].map((entry) => entry.event.event_id), original.map((entry) => entry.event.event_id));
});
