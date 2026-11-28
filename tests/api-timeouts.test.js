'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { timeoutFor, DEFAULT_TIMEOUT_MS } = require('../web/lib/api-timeouts.js');

test('long server budgets get the long client tier', () => {
  assert.equal(timeoutFor('/proxy/start'), 420000);
  assert.equal(timeoutFor('/proxy/stop'), 200000);
  assert.equal(timeoutFor('/chain'), 45000);
  assert.equal(timeoutFor('/chain/refresh'), 45000);
  assert.equal(timeoutFor('/cloud-config'), 45000);
  assert.equal(timeoutFor('/warp/register'), 35000);
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
    '/proxy/stop': 129000, // 120s 热点还原 + TUN/核心/路由拆除
    '/chain': 30000,
    '/cloud-config': 20000,
    '/warp/register': 20000, // warp.NewClient 的 HTTP 超时
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

// start 的预算是**多段之和**，不是单次网络往返：残留自愈 120s + 热点探测 30s +
// 切换 120s + 回滚 60s，再加就绪/快照/DNS/配置自检。按 ×1.5 会白等 200 秒，
// 所以这条只要求覆盖"最坏 + 10s 余量"——但绝不能小于最坏。
test('start timeout covers the stacked stage budgets', () => {
  const worst =
    120000 + // 残留快照自愈
    30000 + // 热点探测
    120000 + // 热点切换
    60000 + // 切换失败回滚
    15000 + // 内核就绪
    30000 + // DNS 修复
    15000 + // 链式/备用配置自检
    10000 + // 网络快照
    5000; // 权限检查
  assert.ok(
    timeoutFor('/proxy/start') >= worst + 10000,
    `start timeout ${timeoutFor('/proxy/start')}ms must cover the ${worst}ms worst case plus 10s`
  );
});

// 前缀不能误伤：/status 不该被 /stat 之类的规则吃掉（规则都带 \b）。
test('prefix matching does not leak into lookalike paths', () => {
  assert.equal(timeoutFor('/proxy/startup'), DEFAULT_TIMEOUT_MS);
  assert.equal(timeoutFor('/noticeboard'), DEFAULT_TIMEOUT_MS);
  assert.equal(timeoutFor('/warp/registered'), DEFAULT_TIMEOUT_MS);
});
