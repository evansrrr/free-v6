# Default blacklist

`blacklist.txt` (gitignored) lists the external domains freev6 blocks by
default. It is embedded into `freev6-helper` with `go:embed` at build time,
so every copy installed from the setup package ships with this list.

## Format

- One domain per line — blocks the domain and all of its subdomains
  (`DOMAIN-SUFFIX,<domain>,REJECT`).
- Blank lines and lines starting with `#` are ignored.
- Entries must be plain lowercase domains; CIDRs are rejected and abort
  the proxy start with an error.

## Behaviour & precedence

- The embedded list applies to every fresh install out of the box.
- A **non-empty** `blacklist` array in `config/settings.json` (app root)
  overrides the embedded list at runtime.
- The in-app developer-mode switch (or `"devMode": true` in settings.json)
  disables all blacklist blocking without rebuilding.

## Workflow

1. Edit `blacklist.txt` locally and commit it — the file is tracked, so
   clones and the release workflow build with the real default list
   (no repository secret needed).
2. `cargo tauri build` — the before-build step recompiles `freev6-helper`
   and embeds the current file into the binary that goes into the installer.
3. A missing `blacklist.txt` (e.g. a partial checkout) still builds fine
   with an empty default list (`go test` / CI stay green).
