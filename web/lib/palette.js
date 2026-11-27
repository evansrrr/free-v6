/*
 * Colour maths for the dynamic palette, extracted so it can be tested without
 * a DOM. Pure functions only: hex in, hex out.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6Palette = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const HEX6 = /^#[0-9a-f]{6}$/i;

  function clamp(v, lo, hi) {
    return Math.min(hi, Math.max(lo, v));
  }

  function hexToHsl(hex) {
    if (!HEX6.test(String(hex || ''))) return null;
    const value = parseInt(hex.slice(1), 16);
    const r = (value >> 16 & 255) / 255;
    const g = (value >> 8 & 255) / 255;
    const b = (value & 255) / 255;
    const max = Math.max(r, g, b);
    const min = Math.min(r, g, b);
    const l = (max + min) / 2;
    if (max === min) return { h: 0, s: 0, l };
    const d = max - min;
    const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    let h;
    if (max === r) h = (g - b) / d + (g < b ? 6 : 0);
    else if (max === g) h = (b - r) / d + 2;
    else h = (r - g) / d + 4;
    return { h: h * 60, s, l };
  }

  function hslToHex(h, s, l) {
    h = ((h % 360) + 360) % 360;
    const c = (1 - Math.abs(2 * l - 1)) * s;
    const x = c * (1 - Math.abs((h / 60) % 2 - 1));
    const m = l - c / 2;
    const [r, g, b] =
      h < 60 ? [c, x, 0] : h < 120 ? [x, c, 0] : h < 180 ? [0, c, x]
        : h < 240 ? [0, x, c] : h < 300 ? [x, 0, c] : [c, 0, x];
    const to = (v) => Math.round((v + m) * 255).toString(16).padStart(2, '0');
    return `#${to(r)}${to(g)}${to(b)}`;
  }

  // Material-You-ish three-family palette derived from one seed colour.
  function derivePalette(seedHex, dark) {
    const hsl = hexToHsl(seedHex);
    if (!hsl) return null;
    const { h, s } = hsl;
    const S = clamp(s < 0.12 ? 0.45 : s, 0.30, 0.90);
    const hex = (hh, ss, ll) => hslToHex(hh, clamp(ss, 0, 1), clamp(ll, 0, 1));
    const T = h + 60;
    if (dark) {
      return {
        '--md-primary': hex(h, S, 0.68),
        '--md-on-primary': hex(h, Math.min(S, 0.45), 0.12),
        '--md-primary-container': hex(h, S * 0.45, 0.30),
        '--md-on-primary-container': hex(h, S, 0.86),
        '--md-secondary': hex(h, S * 0.35, 0.74),
        '--md-on-secondary': hex(h, 0.20, 0.12),
        '--md-secondary-container': hex(h, S * 0.25, 0.30),
        '--md-on-secondary-container': hex(h, S * 0.50, 0.86),
        '--md-tertiary': hex(T, S * 0.50, 0.72),
        '--md-on-tertiary': hex(T, 0.30, 0.12),
        '--md-tertiary-container': hex(T, S * 0.35, 0.30),
        '--md-on-tertiary-container': hex(T, S * 0.60, 0.86),
      };
    }
    return {
      '--md-primary': hex(h, S, 0.42),
      '--md-on-primary': hex(h, Math.min(S, 0.6), 0.99),
      '--md-primary-container': hex(h, S * 0.40, 0.90),
      '--md-on-primary-container': hex(h, S, 0.18),
      '--md-secondary': hex(h, S * 0.30, 0.45),
      '--md-on-secondary': hex(h, 0.30, 0.99),
      '--md-secondary-container': hex(h, S * 0.25, 0.90),
      '--md-on-secondary-container': hex(h, S * 0.50, 0.18),
      '--md-tertiary': hex(T, S * 0.45, 0.45),
      '--md-on-tertiary': hex(T, 0.35, 0.99),
      '--md-tertiary-container': hex(T, S * 0.30, 0.90),
      '--md-on-tertiary-container': hex(T, S * 0.50, 0.18),
    };
  }

  return { clamp, hexToHsl, hslToHex, derivePalette };
});
