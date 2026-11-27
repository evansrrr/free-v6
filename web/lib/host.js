/*
 * Host bridge (Tauri) — the only place that talks to window.__TAURI_*.
 *
 * Every command name is allow-listed. A typo, a refactor that drops a command
 * from the Rust side, or a future XSS calling `invoke('app_quit')` cannot reach
 * anything outside this list. In a browser preview there is no bridge and the
 * promise resolves to undefined, matching the previous tauriInvoke fallback.
 */
(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.FreeV6Host = api;
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const ALLOWED = [
    'window_minimize',
    'window_toggle_maximize',
    'window_maximized',
    'window_close',
    'window_start_dragging',
    'window_show',
    'app_quit',
    'get_api_token',
    'get_helper_port',
    'get_memory_usage',
    'hotkey_set',
    'set_background',
    'update_apply',
    'plugin:opener|open_url',
  ];

  function bridge() {
    return window.__TAURI__?.core || window.__TAURI_INTERNALS__ || null;
  }

  function isAvailable() {
    const b = bridge();
    return Boolean(b && typeof b.invoke === 'function');
  }

  async function invoke(command, args) {
    if (!ALLOWED.includes(command)) {
      return Promise.reject(new Error(`不允许的宿主命令: ${command}`));
    }
    const b = bridge();
    return b && typeof b.invoke === 'function'
      ? b.invoke(command, args)
      : Promise.resolve(undefined);
  }

  return { invoke, isAvailable, ALLOWED };
});
