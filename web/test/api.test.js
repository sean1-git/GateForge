import test from 'node:test';
import assert from 'node:assert/strict';
import { request, totals } from '../src/api.js';
test('aggregates gateway metrics without averaging averages', () => {
  assert.deepEqual(totals([{ requests: 2, errors: 1, cache_hits: 1, cache_misses: 1, duration_seconds: 1 }, { requests: 8, errors: 0, cache_hits: 6, cache_misses: 2, duration_seconds: 3 }]), { requests: 10, errors: 1, hits: 7, misses: 3, seconds: 4 });
});
test('reports revision conflicts instead of treating a failed save as success', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: false, status: 409, json: async () => ({ error: 'configuration changed; refresh before saving' }) });
  try { await assert.rejects(request('test', 'config'), /refresh before saving/); } finally { globalThis.fetch = original; }
});
