'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { timeoutFor, DEFAULT_TIMEOUT_MS } = require('../web/lib/api-timeouts.js');

test('long server budgets get the long client tier', () => {
  assert.equal(timeoutFor('/proxy/start'), 180000);
  assert.equal(timeoutFor('/proxy/stop'), 180000);
  assert.equal(timeoutFor('/chain'), 45000);
  assert.equal(timeoutFor('/chain/refresh'), 45000);
  assert.equal(timeoutFor('/cloud-config'), 45000);
  assert.equal(timeoutFor('/runtime/check'), 30000);
  assert.equal(timeoutFor('/update/latest'), 25000);
  assert.equal(timeoutFor('/notice'), 20000);
});

test('async-short endpoints do not wait for the default tier', () => {
  assert.equal(timeoutFor('/update/download'), 10000);
  assert.equal(timeoutFor('/runtime/download'), 10000);
});

test('unknown endpoints fall back to the default', () => {
  assert.equal(timeoutFor('/status'), DEFAULT_TIMEOUT_MS);
  assert.equal(timeoutFor('/settings'), DEFAULT_TIMEOUT_MS);
  assert.equal(timeoutFor(undefined), DEFAULT_TIMEOUT_MS);
});

// 客户端超时必须大于服务端预算，否则用户看到的是"没有更新"而不是"检查失败"。
// 这里把关系写死：预算一旦调整，档位必须跟着调。
test('client timeout stays above the server budget', () => {
  const budgets = {
    '/proxy/start': 120000, // ICS 最坏
    '/chain': 30000,
    '/cloud-config': 20000,
    '/runtime/check': 20000,
    '/update/latest': 15000,
    '/notice': 12000,
  };
  for (const [path, budget] of Object.entries(budgets)) {
    assert.ok(
      timeoutFor(path) >= budget * 1.5,
      `${path}: client ${timeoutFor(path)}ms must be at least 1.5x the ${budget}ms server budget`
    );
  }
});

// 前缀不能误伤：/status 不该被 /stat 之类的规则吃掉（规则都带 \b）。
test('prefix matching does not leak into lookalike paths', () => {
  assert.equal(timeoutFor('/proxy/startup'), DEFAULT_TIMEOUT_MS);
  assert.equal(timeoutFor('/noticeboard'), DEFAULT_TIMEOUT_MS);
});
