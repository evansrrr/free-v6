# freev6 product roadmap

## Product target

The release target is a Windows 10/11 installer with one user-facing desktop application:

- Tauri GUI and tray process for settings and status.
- Go helper for WARP registration, credential storage, mihomo lifecycle, health checks, and privileged network operations.
- mihomo Alpha as a managed runtime binary, downloaded only from a trusted release source after user confirmation.
- No WARP desktop client dependency for the MASQUE path.

The first release target is Windows x64. ARM64 and macOS remain separate follow-up targets because TUN, installer, and privilege behavior differ.

## Installed layout

The installer chooses a root such as `%LOCALAPPDATA%\freev6` for user-writable data, or a user-selected equivalent:

```text
freev6/
  freev6.exe                  # Tauri GUI and tray entry point
  freev6-helper.exe           # Go helper, started with the required elevation boundary
  runtime/
    mihomo-windows-amd64-v3.exe
    mihomo.version.json
  config/
    settings.json             # non-secret user settings
  state/
    warp.json                 # encrypted/protected device state
    mihomo.yaml               # generated private runtime config
    mihomo.log
    mihomo.pid
  cache/
    ruleset/
    downloads/
  logs/
```

The exact paths must be resolved from the executable/install root, never from the current working directory. Runtime files and secrets must not be written beside source files or into a shared system directory.

## Runtime contract

The helper owns the following state machine:

```text
Stopped
  -> Preparing       validate install, binary, WARP state, privileges
  -> Starting        render config, start mihomo, wait for controller
  -> Running         poll process, controller, and IPv6 egress
  -> Stopping        disable TUN, restore DNS/routes, stop process
  -> Failed           preserve diagnostics and restore the previous snapshot
```

Every transition is idempotent. A stale PID, crashed mihomo process, failed TUN initialization, or interrupted shutdown must converge to `Stopped` without leaving the user's route or DNS configuration modified.

The initial authenticated loopback API should expose:

- `GET /api/v1/status`: helper, mihomo, TUN, WARP registration, network and health state.
- `POST /api/v1/warp/register`: register once and save protected state.
- `POST /api/v1/proxy/start`: start with the saved mode and campus CIDRs.
- `POST /api/v1/proxy/stop`: stop and restore the network snapshot.
- `GET /api/v1/settings`: return non-secret settings.
- `PUT /api/v1/settings`: validate and persist mode, campus CIDRs, autostart and update settings.
- `GET /api/v1/runtime`: report mihomo presence, version, architecture and checksum.
- `POST /api/v1/runtime/download`: download a verified runtime after explicit user confirmation.
- `GET /api/v1/updates/latest`: check application and runtime update metadata.
- `POST /api/v1/updates/apply`: stage an update and hand off to the installer/updater process.
- `GET /api/v1/logs`: return bounded recent diagnostics, never private keys.

Before public release, replace the loopback bearer token with a Windows named pipe or keep the token plus strict ACLs and origin checks. The GUI must never receive the WARP private key.

## Configuration rules

- Default mode is `tun + rule`.
- Optional mode is `global`; it still has no external `DIRECT` route.
- Only explicitly configured campus CIDRs and LAN routes may bypass WARP.
- The GUI accepts individual CIDRs, validates them in the helper, deduplicates them, and shows the effective rules before activation.
- Generated mihomo configuration remains private and is recreated from protected state and settings.
- The GUI must warn before activation if no campus CIDR has been configured and must show that all non-LAN traffic will use WARP.

## Execution milestones

### M2.1 Runtime ownership and health

- [x] Discover the architecture-matching mihomo Alpha binary in `runtime/` with a development `state/` fallback.
- [x] Wait for the loopback mihomo controller after process startup and clean up on readiness failure.
- [x] Verify the controller `/version` response and required node-selection proxy group before reporting startup success.
- [x] Probe a real IPv6 egress address before reporting the proxy as started.
- [x] Resolve CLI runtime paths from the installation root; carry the same contract into the helper process.
- Verify a signed manifest or pinned SHA-256 before execution.
- Add mihomo `external-controller` on loopback with a generated secret.
- Start, poll controller readiness, check proxy groups and IPv6 egress, and classify failures.
- [x] Handle stale and invalid PID files during start, stop, and status.
- [ ] Handle mihomo crashes and stop timeout escalation.

Checkpoint: CLI can start and stop the managed runtime repeatedly without manual path arguments.

### M3.1 Windows network lifecycle

- [x] Add Windows PowerShell capture and parser for physical interfaces, default gateways, and DNS.
- [x] Check administrator privilege before the CLI starts mihomo TUN.
- [x] Persist the snapshot and restore DNS on activation failure and normal stop.
- [ ] Verify mihomo TUN route cleanup and add route restoration fallback on a disposable Windows test machine.
- Add campus CIDR validation and explicit route/DNS exclusions.
- Restore the captured snapshot on stop, crash, GUI exit, and failed startup.
- Add Windows integration tests for command construction and snapshot parsing; run destructive network tests only on a disposable test machine.

Checkpoint: repeated activation/deactivation leaves the pre-activation network state unchanged.

### M3.2 Protected state and settings

- Replace plaintext WARP private state with DPAPI-protected storage bound to the current user or machine.
- Store non-secret settings separately and version the schema.
- Add migration from the current `state/warp.json` format with a one-time import warning.
- Ensure logs and API responses redact keys, tokens, and generated config contents.

Checkpoint: uninstall/upgrade does not silently delete credentials, and diagnostics contain no secrets.

### M4.1 Tauri GUI

- [x] Add the initial Tauri 2 shell, tray lifecycle, and Material You frontend to this repository.
- [x] Start the Go helper as a Tauri sidecar and connect read-only status/runtime/settings views.
- [ ] Add authenticated helper API actions for WARP registration and proxy start/stop.
- On launch, check helper availability, install root, architecture, and mihomo runtime presence.
- Provide first-run flow: download runtime, register WARP, choose campus CIDRs, test readiness, activate.
- Provide dashboard: proxy state, TUN state, selected mode, selected group/node, IPv6 egress, errors and bounded logs.
- Provide settings: rule/global mode, campus CIDRs, startup behavior, DNS policy, update channel.
- Keep privileged actions and all secret-bearing operations in the helper.

Checkpoint: a clean machine can install, launch, complete first-run setup, activate, deactivate, and relaunch without a terminal.

### M4.2 Updates and packaging

- Use Tauri NSIS packaging for the GUI and helper.
- Ship no mihomo binary in the initial installer unless licensing and artifact size are acceptable; offer verified first-run download.
- Publish application and mihomo manifest metadata with version, architecture, URL, SHA-256, and release notes.
- Stage updates outside the running installation, verify them, then replace files after helper shutdown.
- Add rollback for failed application updates and preserve user state.
- Build Windows x64 in CI, run Go/Rust tests, validate installer contents, and upload checksums.

Checkpoint: clean install, upgrade, rollback, and uninstall are tested on Windows 10 and 11.

## Git workflow

Keep changes in small, reviewable commits. Suggested checkpoints:

1. `docs: define runtime and desktop product contract`
2. `feat: add mihomo runtime discovery and manifest verification`
3. `feat: add authenticated helper lifecycle API`
4. `feat: add Windows network snapshot and restoration`
5. `feat: protect WARP state with DPAPI`
6. `feat: add Tauri first-run and proxy dashboard`
7. `feat: add verified runtime and application updates`
8. `build: package Windows installer and CI artifacts`

Before each checkpoint: `gofmt`, `go test ./...`, `go vet ./...`, Rust format/check, and `git diff --check`. Do not commit generated secrets, runtime binaries, generated configs, or local logs.

## Known product risks

- TUN and route restoration are the highest-risk area and require failure-path testing with administrator privileges.
- A downloaded mihomo binary must be verified and its license/redistribution terms reviewed before release.
- Campus CIDRs are institution-specific; ship no guessed public ranges as bypass rules.
- Windows Defender, unsigned installers, and driver/TUN permissions need a clear first-run error path.
- Automatic updates must never replace a running helper or destroy protected user state.
