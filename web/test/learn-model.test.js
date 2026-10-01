import test from 'node:test';
import assert from 'node:assert/strict';
import { initialSimulation, simulate } from '../src/learn-model.js';

test('a request reaches only the selected service', () => {
  const result = simulate(initialSimulation(), { scene: 'journey', route: 1 }, 3);
  assert.deepEqual(result.counts, [0, 3, 0]);
  assert.equal(result.sent, result.accepted);
});
test('one budget stops excess traffic before it reaches services', () => {
  const result = simulate(initialSimulation(), { scene: 'limits', centralized: true }, 12);
  assert.deepEqual(result.counts, [2, 2, 2]);
  assert.equal(result.blocked, 6);
  assert.ok(result.recent.slice(6).every(event => event.target === null && !event.accepted));
});
test('separate budgets produce different outcomes for the same client', () => {
  const result = simulate(initialSimulation(), { scene: 'limits', centralized: false }, 12);
  assert.deepEqual(result.counts, [2, 4, 4]);
  assert.equal(result.blocked, 2);
});
test('round robin and 4:1:1 have the illustrated proportions', () => {
  assert.deepEqual(simulate(initialSimulation(), { scene: 'balance' }, 12).counts, [4, 4, 4]);
  assert.deepEqual(simulate(initialSimulation(), { scene: 'balance', weighted: true }, 12).counts, [8, 2, 2]);
});
test('offline server receives no traffic in either strategy', () => {
  for (const weighted of [true, false]) {
    const result = simulate(initialSimulation(), { scene: 'balance', weighted, offline: true }, 12);
    assert.deepEqual(result.counts, [0, 6, 6]);
    assert.equal(result.sent, result.accepted + result.blocked);
  }
});
test('individual steps match a burst and input state remains unchanged', () => {
  const initial = initialSimulation();
  const settings = { scene: 'balance', weighted: true };
  let stepped = initial;
  for (let i = 0; i < 12; i++) stepped = simulate(stepped, settings);
  assert.deepEqual(stepped.counts, simulate(initial, settings, 12).counts);
  assert.deepEqual(initial, initialSimulation());
});
