'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { createMeter } = require('../web/traffic.js');

// Controllable monotonic clock; the meter must never read wall time itself.
function clocked(start = 1000) {
  let now = start;
  return {
    meter: createMeter({ now: () => now }),
    advance(ms) { now += ms; },
  };
}

test('first observation is a baseline only', () => {
  const { meter, advance } = clocked();
  assert.deepEqual(meter.observe(0, 0), { kind: 'first' });
  assert.equal(meter.ready, true);

  advance(1000);
  assert.deepEqual(meter.observe(1024, 102400), {
    kind: 'sample',
    deltaUp: 1024,
    deltaDown: 102400,
    upRate: 1024,
    downRate: 102400,
    elapsedMs: 1000,
  });
});

test('idle interval emits zero rates without changing totals', () => {
  const { meter, advance } = clocked();
  meter.observe(0, 0);
  advance(1000);
  meter.observe(500, 5000);
  advance(1000);
  assert.deepEqual(meter.observe(500, 5000), {
    kind: 'sample',
    deltaUp: 0,
    deltaDown: 0,
    upRate: 0,
    downRate: 0,
    elapsedMs: 1000,
  });
});

test('counters going backwards re-baseline instead of clamping', () => {
  const { meter, advance } = clocked();
  meter.observe(0, 0);
  advance(1000);
  meter.observe(1024, 102400);

  advance(1000);
  assert.deepEqual(meter.observe(10, 100), { kind: 'reset' });

  // Traffic forwarded after the restart is counted from the new baseline.
  advance(1000);
  assert.deepEqual(meter.observe(110, 1100), {
    kind: 'sample',
    deltaUp: 100,
    deltaDown: 1000,
    upRate: 100,
    downRate: 1000,
    elapsedMs: 1000,
  });
});

test('slow sample keeps the full delta and normalizes the rate', () => {
  const { meter, advance } = clocked();
  meter.observe(0, 0);
  advance(1000);
  meter.observe(1024, 102400);

  // Poll delayed by 10s: totals must be exact, chart/speed are per-second.
  advance(10000);
  assert.deepEqual(meter.observe(2024, 202400), {
    kind: 'sample',
    deltaUp: 1000,
    deltaDown: 100000,
    upRate: 100,
    downRate: 10000,
    elapsedMs: 10000,
  });
});

test('invalid input leaves the baseline untouched', () => {
  const { meter, advance } = clocked();
  assert.deepEqual(meter.observe(NaN, 0), { kind: 'invalid' });
  assert.deepEqual(meter.observe(0, undefined), { kind: 'invalid' });
  assert.equal(meter.ready, false);

  assert.deepEqual(meter.observe(0, 0), { kind: 'first' });

  advance(1000);
  assert.deepEqual(meter.observe(Infinity, 1), { kind: 'invalid' });
  advance(1000);
  // The invalid observation did not move the baseline or the clock.
  assert.deepEqual(meter.observe(100, 1000), {
    kind: 'sample',
    deltaUp: 100,
    deltaDown: 1000,
    upRate: 50,
    downRate: 500,
    elapsedMs: 2000,
  });
});

test('reset() returns the meter to the unbaselined state', () => {
  const { meter, advance } = clocked();
  meter.observe(0, 0);
  advance(1000);
  meter.observe(1024, 102400);
  meter.reset();
  assert.equal(meter.ready, false);
  assert.deepEqual(meter.observe(99, 999), { kind: 'first' });
});
