/*
 * Version comparison, extracted from app.js so CI can test it without a DOM.
 * Same UMD shape as traffic.js / ipv6gate.js: a classic script that also works
 * under `require()` in node:test.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6Version = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  function splitVersion(value) {
    const [core, ...pre] = String(value || '').split('-');
    const parts = core.split('.').map((n) => parseInt(n, 10) || 0);
    return [parts, pre.join('-')];
  }

  // semver order: numeric core first; release > prerelease; prerelease segments
  // compare numerically when both are numbers, otherwise lexically.
  function cmpVersion(a, b) {
    const [pa, preA] = splitVersion(a);
    const [pb, preB] = splitVersion(b);
    for (let i = 0; i < 3; i++) {
      const delta = (pa[i] || 0) - (pb[i] || 0);
      if (delta !== 0) return delta;
    }
    if (preA === preB) return 0;
    if (!preA) return 1;
    if (!preB) return -1;
    const sa = preA.split('.');
    const sb = preB.split('.');
    for (let i = 0; i < Math.max(sa.length, sb.length); i++) {
      if (sa[i] === undefined) return -1;
      if (sb[i] === undefined) return 1;
      if (sa[i] === sb[i]) continue;
      const na = parseInt(sa[i], 10);
      const nb = parseInt(sb[i], 10);
      if (!Number.isNaN(na) && !Number.isNaN(nb)) return na - nb;
      if (!Number.isNaN(na)) return -1;
      if (!Number.isNaN(nb)) return 1;
      return sa[i] < sb[i] ? -1 : 1;
    }
    return 0;
  }

  return { splitVersion, cmpVersion };
});
