/* IPv6 gate decision core.
 *
 * The start-up gate answers one question: may this machine run 免流, and if not,
 * why. The answer depends only on the helper's /ipv6/probe payload, so it lives
 * here as a pure function — no DOM, no logging, no app state — and both the UI
 * and node:test load the same code.
 *
 * Rules, in order:
 *   1. no address at all        -> block ("no IPv6 address")
 *   2. no campus-prefix address -> pass a non-campus address; the caller turns
 *                                  that into "this network cannot run 免流"
 *   3. a campus address that was measured as reachable -> pass
 *   4. no campus address measured reachable:
 *        a. any campus address measured as unreachable -> block, naming it
 *        b. otherwise (older helper without egress, or the source address could
 *           not be bound) -> pass as "unverified"
 * Rule 4b matters: this gate is a hint, and the failure it protects against
 * (starting on a dead path) surfaces immediately as a real error. Blocking on
 * missing evidence is the more annoying mistake.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6IPv6Gate = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  // Campus prefixes allowed to run 免流, as /32 first-two-hextet fingerprints:
  // 2001:da8::/32 (CERNET2) and 2001:250::/32.
  const CAMPUS_PREFIX32 = [[0x2001, 0x0da8], [0x2001, 0x0250]];

  // Expand an IPv6 literal to its 8 hextets, or null when unparsable.
  function expandIPv6(ip) {
    const cleaned = String(ip).trim().split('%')[0].toLowerCase();
    if (!/^[0-9a-f:]+$/.test(cleaned) || !cleaned.includes(':')) return null;
    const halves = cleaned.split('::');
    if (halves.length > 2) return null;
    const head = halves[0] ? halves[0].split(':') : [];
    const tail = halves.length === 2 && halves[1] ? halves[1].split(':') : [];
    let groups;
    if (halves.length === 2) {
      const fill = 8 - head.length - tail.length;
      if (fill < 1) return null; // '::' must stand for at least one group
      groups = [...head, ...Array(fill).fill('0'), ...tail];
    } else {
      groups = head;
      if (groups.length !== 8) return null;
    }
    if (groups.length !== 8) return null;
    const nums = groups.map((g) => (/^[0-9a-f]{1,4}$/.test(g) ? parseInt(g, 16) : NaN));
    return nums.some(Number.isNaN) ? null : nums;
  }

  function isInCampusIPv6(ip) {
    const groups = expandIPv6(ip);
    if (!groups) return false;
    return CAMPUS_PREFIX32.some(([a, b]) => groups[0] === a && groups[1] === b);
  }

  // The helper's addresses are objects (per-address verdict) in new builds and
  // plain strings in older ones; older ones also only carry a top-level egress.
  function normalizeProbeAddresses(res) {
    const raw = Array.isArray(res?.addresses) ? res.addresses : [];
    const list = raw
      .map((item) => (typeof item === 'string' ? { ip: item } : item))
      .filter((item) => item && item.ip);
    if (!list.length && res?.ip) list.push({ ip: res.ip });
    if (list.length && list.every((item) => typeof item.egress !== 'boolean') && typeof res?.egress === 'boolean') {
      for (const item of list) item.egress = res.egress;
    }
    return list;
  }

  function noteFor(item, res, extra) {
    return [item.alias || res?.source, extra].filter(Boolean).join(' · ');
  }

  /**
   * @returns {{action: 'pass'|'block', ip?: string, note?: string,
   *            messages?: string[], error?: string}}
   */
  function decideIPv6Gate(res) {
    const list = normalizeProbeAddresses(res);
    if (!list.length) {
      return { action: 'block', error: res?.error || '本机没有 IPv6 地址' };
    }

    const campus = list.filter((item) => isInCampusIPv6(item.ip));
    if (!campus.length) {
      // No campus address: hand back something so the caller can say
      // "this network cannot run 免流" with an actual address in the log.
      const usable = list.find((item) => item.egress === true) || list[0];
      return { action: 'pass', ip: usable.ip, note: '', messages: [] };
    }

    const usable = campus.find((item) => item.egress === true);
    if (!usable) {
      const blocked = campus.filter((item) => item.egress === false && !item.unverified);
      if (!blocked.length) {
        const unknown = campus[0];
        return {
          action: 'pass',
          ip: unknown.ip,
          note: noteFor(unknown, res, '出站未验证'),
          messages: [`IPv6 出站未验证（${unknown.ip}），按可用继续`],
        };
      }
      const first = blocked[0];
      return {
        action: 'block',
        error: `校园 IPv6 地址 ${first.ip} 出站不通${first.detail ? `（${first.detail}）` : ''}`,
      };
    }

    const messages = [];
    if (usable.scope === 'campus') {
      // Only campus targets answered: packets leave, the off-campus path is
      // unproven. Advisory only.
      messages.push(`IPv6 出站仅校内可达（${usable.target || '校园检测点'}），校外路径未验证`);
    }

    // egress=true + unverified means the source address could not be bound and
    // the system-chosen route succeeded: that proves "this machine has an
    // exit", not "this campus address can use it". Say so instead of quoting a
    // round-trip time for a path that was never tested.
    let note;
    if (usable.unverified) {
      note = noteFor(usable, res, '出站未验证（按系统路由）');
    } else {
      const rtt = usable.rttMs ?? res?.rttMs;
      note = noteFor(usable, res, rtt != null ? `出站 ${rtt}ms` : '');
    }

    const attempts = usable.attempts || res?.attempts || 0;
    if (attempts > 1) messages.push(`IPv6 出站检测重试 ${attempts - 1} 次后通过`);

    return { action: 'pass', ip: usable.ip, note, messages };
  }

  return { expandIPv6, isInCampusIPv6, normalizeProbeAddresses, decideIPv6Gate, CAMPUS_PREFIX32 };
});
