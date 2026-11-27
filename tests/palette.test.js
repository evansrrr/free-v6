'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { hexToHsl, hslToHex, derivePalette } = require('../web/lib/palette.js');

test('hexToHsl round-trips a six-digit colour', () => {
  const hsl = hexToHsl('#7eaaff');
  assert.ok(hsl);
  assert.equal(hslToHex(hsl.h, hsl.s, hsl.l), '#7eaaff');
});

test('hexToHsl rejects short and non-hex input instead of guessing', () => {
  assert.equal(hexToHsl('#abc'), null);
  assert.equal(hexToHsl('red'), null);
  assert.equal(hexToHsl('#12345'), null);
  assert.equal(hexToHsl(''), null);
});

test('derivePalette returns the full twelve-variable set for both modes', () => {
  const dark = derivePalette('#7eaaff', true);
  const light = derivePalette('#7eaaff', false);
  for (const palette of [dark, light]) {
    assert.equal(Object.keys(palette).length, 12);
    for (const [name, value] of Object.entries(palette)) {
      assert.match(name, /^--md-/);
      assert.match(value, /^#[0-9a-f]{6}$/);
    }
  }
  assert.notEqual(dark['--md-primary'], light['--md-primary']);
});

test('grey seeds still produce a usable chroma', () => {
  const palette = derivePalette('#808080', true);
  assert.ok(palette);
  assert.notEqual(palette['--md-primary'], '#808080');
});

test('derivePalette returns null for an invalid seed', () => {
  assert.equal(derivePalette('#abc', true), null);
});
