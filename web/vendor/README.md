# Vendored frontend dependencies

Static ES-module copies used by the freev6 shell (no bundler; loaded via the
import map in `index.html`).

## Contents

| Path            | Source                                              | License       |
| --------------- | --------------------------------------------------- | ------------- |
| `material-web/` | [material-components/material-web](https://github.com/material-components/material-web) **v2.5.0** – only the components freev6 uses (dependency closure of the entries below) | Apache-2.0 |
| `lit/`, `lit-html/`, `lit-element/`, `@lit/` | [lit](https://github.com/lit/lit) (version pinned by `@material/web` 2.5.0) – runtime required by the components | BSD-3-Clause |
| `tslib/`        | [tslib](https://github.com/tslibio/tslib) – TS helper runtime imported by the compiled components | 0BSD |

Components copied for freev6: `button`, `fab`, `iconbutton`, `progress`
(circular), `chips` (filter), `select`, `textfield`, plus their transitive
internal dependencies (`internal/`, `field/`, `menu/`, `list/`, `ripple/`,
`focus/`, `elevation/`, `labs/behaviors`, `typography/`).

Entry module: `../material.js` registers all `md-*` elements used by the UI.

## Regenerating

1. Build the `@material/web` 2.5.0 checkout (tsc + sass; on Windows run the
   `find | xargs` wireit steps manually with PowerShell equivalents).
2. Re-run the closure-copy script (`vendor-to-freev6.mjs` in that checkout),
   then copy `lit`, `lit-html`, `lit-element`, `@lit/reactive-element` and
   `tslib` from its `node_modules`.
