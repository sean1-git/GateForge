import test from 'node:test';
import assert from 'node:assert/strict';
import { request, totals, actionGate } from '../src/api.js';

test('session requests send CSRF and same-origin cookies without bearer credentials', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/admin/api/keys');
    assert.equal(options.credentials, 'same-origin');
    assert.equal(options.headers['X-GateForge-CSRF'], 'csrf-test-value');
    assert.equal(options.headers.Authorization, undefined);
    return { status: 204 };
  };
  try { await request({csrf_token:'csrf-test-value'}, 'keys', {method:'POST'}); } finally { globalThis.fetch=original; }
});

test('session discovery sends no empty or shared bearer token', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/admin/auth/session');
    assert.equal(options.headers.Authorization, undefined);
    return {ok:true,status:200,json:async()=>({identity:{email:'owner@example.com'}})};
  };
  try { await request('', 'session', {basePath:'/admin/auth/'}); } finally { globalThis.fetch=original; }
});
test('aggregates gateway metrics without averaging averages', () => {
  assert.deepEqual(totals([{ requests: 2, errors: 1, cache_hits: 1, cache_misses: 1, duration_seconds: 1 }, { requests: 8, errors: 0, cache_hits: 6, cache_misses: 2, duration_seconds: 3 }]), { requests: 10, errors: 1, hits: 7, misses: 3, seconds: 4 });
});
test('handles an HTML proxy error without a JSON parsing error', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => ({ ok:false, status:502, json:async()=>{throw new SyntaxError('HTML');} });
  try { await assert.rejects(request('test','keys'), /502/); } finally { globalThis.fetch=original; }
});
test('aborts hung requests, reports uncertain writes, and never retries', async () => {
  const original = globalThis.fetch; let calls=0;
  globalThis.fetch = (_url,{signal}) => { calls++; return new Promise((_resolve,reject)=>signal.addEventListener('abort',()=>reject(new DOMException('Aborted','AbortError')))); };
  try { await assert.rejects(request('test','keys',{method:'POST',timeoutMs:5}), /may have completed/); assert.equal(calls,1); } finally { globalThis.fetch=original; }
});
test('distinguishes explicit cancellation from a request failure', async () => {
  const original=globalThis.fetch; const c=new AbortController(); c.abort();
  globalThis.fetch=async()=>{throw new DOMException('Aborted','AbortError');};
  try { await assert.rejects(request('test','metrics',{signal:c.signal}), {name:'AbortError'}); } finally {globalThis.fetch=original;}
});
test('blocks simultaneous submissions and unlocks after failure', async () => {
  const gate=actionGate(); let release; let calls=0;
  const first=gate(()=>{calls++; return new Promise(resolve=>{release=resolve;});});
  await gate(()=>{calls++;}); assert.equal(calls,1); release(); await first;
  await assert.rejects(gate(async()=>{throw Error('failed');}), /failed/);
  await gate(()=>{calls++;}); assert.equal(calls,2);
});
test('reports revision conflicts instead of treating a failed save as success', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: false, status: 409, json: async () => ({ error: 'configuration changed; refresh before saving' }) });
  try { await assert.rejects(request('test', 'config'), /refresh before saving/); } finally { globalThis.fetch = original; }
});
