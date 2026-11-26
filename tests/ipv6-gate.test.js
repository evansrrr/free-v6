'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { decideIPv6Gate, isInCampusIPv6, normalizeProbeAddresses } = require('../web/ipv6gate.js');

const CAMPUS = '2001:da8:208:38::1';
const CAMPUS_2 = '2001:250:1006::1';
const HOME = '2408:8207:1234::2';

test('passes the campus address that can send', () => {
  const decision = decideIPv6Gate({
    ok: true,
    egress: true,
    addresses: [{ ip: CAMPUS, alias: 'WiFi', egress: true, rttMs: 4, scope: 'public', attempts: 1 }],
  });
  assert.equal(decision.action, 'pass');
  assert.equal(decision.ip, CAMPUS);
  assert.equal(decision.note, 'WiFi · 出站 4ms');
  assert.deepEqual(decision.messages, []);
});

// The whole point of the per-address probe: another link getting out does not
// make the campus address usable.
test('refuses when the campus address is unreachable but another link works', () => {
  const decision = decideIPv6Gate({
    ok: true,
    egress: true,
    ip: HOME,
    addresses: [
      { ip: CAMPUS, alias: 'WiFi', egress: false, detail: '[2400:3200::1] 均不可达' },
      { ip: HOME, alias: 'Ethernet', egress: true, rttMs: 3 },
    ],
  });
  assert.equal(decision.action, 'block');
  assert.equal(decision.error, `校园 IPv6 地址 ${CAMPUS} 出站不通（[2400:3200::1] 均不可达）`);
});

test('campus-only reachability passes with a warning', () => {
  const decision = decideIPv6Gate({
    ok: true,
    addresses: [{
      ip: CAMPUS, alias: 'WiFi', egress: true, rttMs: 2, scope: 'campus', target: '[2001:da8:215:4009::23]',
    }],
  });
  assert.equal(decision.action, 'pass');
  assert.deepEqual(decision.messages, ['IPv6 出站仅校内可达（[2001:da8:215:4009::23]），校外路径未验证']);
  assert.equal(decision.note, 'WiFi · 出站 2ms');
});

// Bind failure falls back to the system-chosen route: that proves the machine
// has an exit, not that this address can use it. Do not quote an RTT for it.
test('unverified source address does not claim a measured round trip', () => {
  const decision = decideIPv6Gate({
    ok: true,
    addresses: [{ ip: CAMPUS, alias: 'WiFi', egress: true, unverified: true, rttMs: 5 }],
  });
  assert.equal(decision.action, 'pass');
  assert.equal(decision.note, 'WiFi · 出站未验证（按系统路由）');
});

test('unverified failure passes with a log line instead of blocking', () => {
  const decision = decideIPv6Gate({
    ok: true,
    addresses: [{ ip: CAMPUS, alias: 'WiFi', egress: false, unverified: true }],
  });
  assert.equal(decision.action, 'pass');
  assert.equal(decision.ip, CAMPUS);
  assert.deepEqual(decision.messages, [`IPv6 出站未验证（${CAMPUS}），按可用继续`]);
});

// a018c93 gave ip/source only; no egress field at all is "unknown", not "dead".
test('missing egress field is treated as unknown', () => {
  const decision = decideIPv6Gate({ ok: true, ip: CAMPUS, source: 'WiFi' });
  assert.equal(decision.action, 'pass');
  assert.equal(decision.ip, CAMPUS);
  assert.deepEqual(decision.messages, [`IPv6 出站未验证（${CAMPUS}），按可用继续`]);
});

test('older helper with a top-level egress verdict still blocks on failure', () => {
  const decision = decideIPv6Gate({
    ok: true, ip: '', source: '', addresses: [CAMPUS], egress: false, error: '出站不通',
  });
  assert.equal(decision.action, 'block');
  assert.equal(decision.error, `校园 IPv6 地址 ${CAMPUS} 出站不通`);
});

test('an unverified sibling cannot wash out a measured failure', () => {
  const decision = decideIPv6Gate({
    ok: true,
    addresses: [
      { ip: CAMPUS, egress: false, detail: 'x 均不可达' },
      { ip: CAMPUS_2, egress: false, unverified: true },
    ],
  });
  assert.equal(decision.action, 'block');
  assert.equal(decision.error, `校园 IPv6 地址 ${CAMPUS} 出站不通（x 均不可达）`);
});

test('no address at all blocks with the helper message', () => {
  const decision = decideIPv6Gate({ ok: true, addresses: [], egress: false, error: '本机没有全局 IPv6 地址' });
  assert.equal(decision.action, 'block');
  assert.equal(decision.error, '本机没有全局 IPv6 地址');
});

test('no campus address hands back an address so the caller can refuse', () => {
  const decision = decideIPv6Gate({ ok: true, addresses: [{ ip: HOME, egress: true }] });
  assert.equal(decision.action, 'pass');
  assert.equal(decision.ip, HOME);
  assert.equal(decision.note, '');
  assert.equal(isInCampusIPv6(decision.ip), false);
});

test('retry count comes from the chosen address, not the probe summary', () => {
  const decision = decideIPv6Gate({
    ok: true,
    attempts: 3,
    addresses: [
      { ip: CAMPUS, egress: true, rttMs: 9, attempts: 3 },
      { ip: HOME, egress: false, attempts: 1 },
    ],
  });
  assert.deepEqual(decision.messages, ['IPv6 出站检测重试 2 次后通过']);
});

test('normalize accepts both payload shapes', () => {
  assert.deepEqual(normalizeProbeAddresses({ addresses: [CAMPUS] }), [{ ip: CAMPUS }]);
  assert.deepEqual(normalizeProbeAddresses({ ip: CAMPUS }), [{ ip: CAMPUS }]);
  assert.deepEqual(normalizeProbeAddresses({}), []);
  assert.deepEqual(
    normalizeProbeAddresses({ addresses: [CAMPUS], egress: true }).map((item) => item.egress),
    [true],
  );
});

test('campus prefix table', () => {
  assert.equal(isInCampusIPv6(CAMPUS), true);
  assert.equal(isInCampusIPv6(CAMPUS_2), true);
  assert.equal(isInCampusIPv6('2001:0da8:0000:0000:0000:0000:0000:0001'), true);
  assert.equal(isInCampusIPv6(HOME), false);
  assert.equal(isInCampusIPv6('fe80::1'), false);
  assert.equal(isInCampusIPv6('10.0.0.1'), false);
  assert.equal(isInCampusIPv6('not-an-ip'), false);
  assert.equal(isInCampusIPv6('2001:da8:::1'), false);
});
