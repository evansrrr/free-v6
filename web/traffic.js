/* Traffic accounting core.
 *
 * Fed with mihomo's absolute kernel counters (uploadTotal/downloadTotal), it
 * returns deltas and per-second rates. Kept free of DOM and app state so it can
 * be unit-tested with node:test and reused by the UI.
 *
 * Counters are cumulative for the lifetime of the mihomo process. A decrease
 * therefore means a different process instance (stop/start, crash, update):
 * the meter re-baselines instead of clamping, so post-restart traffic is still
 * counted and no negative delta is ever emitted.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6Traffic = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  function defaultNow() {
    if (typeof performance !== 'undefined' && typeof performance.now === 'function') {
      return performance.now();
    }
    return Date.now();
  }

  function createMeter(options) {
    const opts = options || {};
    // Monotonic clock: a wall-clock adjustment must not turn a normal interval
    // into a negative (or absurdly large) rate.
    const now = typeof opts.now === 'function' ? opts.now : defaultNow;
    let lastUp = 0;
    let lastDown = 0;
    let lastAt = 0;
    let ready = false;

    return {
      // Absolute counters in, delta/rate out:
      //   { kind: 'first' }   baseline only, nothing to add
      //   { kind: 'reset' }   counters went backwards, baseline re-established
      //   { kind: 'invalid' } non-numeric input, state untouched
      //   { kind: 'sample', deltaUp, deltaDown, upRate, downRate, elapsedMs }
      observe(upTotal, downTotal) {
        if (!Number.isFinite(upTotal) || !Number.isFinite(downTotal)) {
          return { kind: 'invalid' };
        }

        const at = now();

        // First observation is a baseline only: the counters include traffic
        // from before this window opened, which persisted totals may already
        // hold. Losing at most one window on reload/helper reconnect is
        // deliberate: recovering it would need the core instance id plus the
        // exact flushed counter, and a mismatch would double-count far more.
        if (!ready) {
          ready = true;
          lastUp = upTotal;
          lastDown = downTotal;
          lastAt = at;
          return { kind: 'first' };
        }

        const deltaUp = upTotal - lastUp;
        const deltaDown = downTotal - lastDown;

        if (deltaUp < 0 || deltaDown < 0) {
          lastUp = upTotal;
          lastDown = downTotal;
          lastAt = at;
          return { kind: 'reset' };
        }

        const elapsedMs = Math.max(1, at - lastAt);
        lastUp = upTotal;
        lastDown = downTotal;
        lastAt = at;

        return {
          kind: 'sample',
          deltaUp,
          deltaDown,
          upRate: Math.round((deltaUp * 1000) / elapsedMs),
          downRate: Math.round((deltaDown * 1000) / elapsedMs),
          elapsedMs,
        };
      },

      reset() {
        ready = false;
        lastUp = 0;
        lastDown = 0;
        lastAt = 0;
      },

      get ready() {
        return ready;
      },
    };
  }

  return { createMeter };
});
