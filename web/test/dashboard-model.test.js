import test from 'node:test';
import assert from 'node:assert/strict';
import { filterKeys, keyStatus, routeAuth, routeTargets } from '../src/dashboard-model.js';
const keys = [
  { id: 'first', name: 'Development client', prefixes: ['/users'], revoked: false, expires_at: '2030-01-01' },
  { id: 'second', name: 'Old order client', prefixes: ['/orders'], revoked: true, expires_at: '2030-01-01' },
];
test('key filters distinguish empty results from an empty key list', () => {
  assert.equal(filterKeys(keys, ' /USERS ', true)[0].id, 'first');
  assert.equal(filterKeys(keys, 'order', true).length, 0);
  assert.equal(filterKeys(keys, 'order', false)[0].id, 'second');
  assert.equal(filterKeys(keys, 'second', false)[0].id, 'second');
});
test('revoked and expired credentials never appear active', () => {
  assert.equal(keyStatus(keys[0], Date.parse('2026-10-01')), 'Active');
  assert.equal(keyStatus(keys[0], Date.parse('2030-01-01')), 'Expired');
  assert.equal(keyStatus(keys[1], Date.parse('2031-01-01')), 'Revoked');
});
test('route cards retain public defaults and multiple destinations', () => {
  assert.equal(routeAuth({}), 'Public');
  assert.equal(routeAuth({auth:'either'}), 'Key or JWT');
  assert.deepEqual(routeTargets({upstream:'http://one'}), ['http://one']);
  assert.deepEqual(routeTargets({upstreams:['http://one','http://two']}), ['http://one','http://two']);
});
