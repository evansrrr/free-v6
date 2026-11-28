/*
 * Per-endpoint request timeouts, derived from the helper's own budgets.
 *
 * Why a table instead of one number: a client timeout equal to the server
 * budget is a silent wrong answer. `/update/latest` may legitimately take the
 * full 15s budget; aborting at 15s makes the UI report "no update" while the
 * helper is still deciding. Every entry is therefore >= 1.5x the server budget.
 *
 * Same UMD shape as lib/version.js: a classic script that also works under
 * `require()` in node:test.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6ApiTimeouts = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  // 顺序即优先级：/proxy/start 与 /proxy/stop 共用一个 180s 档（ICS 最坏
  // 120s + 建连/调度余量）。新增接口必须同时登记服务端预算与这里的一档。
  const RULES = [
    [/^\/proxy\/(?:start|stop)\b/, 180000],
    [/^\/chain\b/, 45000],
    [/^\/cloud-config\b/, 45000],
    [/^\/runtime\/check\b/, 30000],
    [/^\/update\/latest\b/, 25000],
    [/^\/notice\b/, 20000],
    [/^\/update\/download\b/, 10000],
    [/^\/runtime\/download\b/, 10000],
  ];

  // 其余接口（/status、/settings、/progress…）服务端都在 1s 内返回。
  const DEFAULT_TIMEOUT_MS = 10000;

  function timeoutFor(path) {
    const value = String(path || '');
    for (let i = 0; i < RULES.length; i += 1) {
      if (RULES[i][0].test(value)) return RULES[i][1];
    }
    return DEFAULT_TIMEOUT_MS;
  }

  return { timeoutFor, DEFAULT_TIMEOUT_MS };
});
