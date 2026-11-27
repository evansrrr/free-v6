'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { splitVersion, cmpVersion } = require('../web/lib/version.js');

test('splitVersion splits core and prerelease', () => {
  assert.deepEqual(splitVersion('v0.5.6-beta.3'), [[0, 5, 6], 'beta.3']);
  assert.deepEqual(splitVersion('0.5.6'), [[0, 5, 6], '']);
});

test('release sorts above its prereleases', () => {
  assert.equal(cmpVersion('0.5.6', '0.5.6-beta.3'), 1);
  assert.equal(cmpVersion('0.5.6-beta.3', '0.5.6'), -1);
});

test('numeric core compares numerically, not lexically', () => {
  assert.equal(cmpVersion('0.5.10', '0.5.9'), 1);
  assert.equal(cmpVersion('0.6.0', '0.5.99'), 1);
});

test('prerelease segments compare numerically when both are numbers', () => {
  assert.ok(cmpVersion('0.5.6-beta.2', '0.5.6-beta.10') < 0);
  assert.ok(cmpVersion('0.5.6-beta.10', '0.5.6-beta.2') > 0);
});

test('prerelease segments fall back to lexical order', () => {
  assert.equal(cmpVersion('0.5.6-alpha.1', '0.5.6-beta.1'), -1);
});

test('equal versions compare equal', () => {
  assert.equal(cmpVersion('0.5.6-beta.3', '0.5.6-beta.3'), 0);
});
