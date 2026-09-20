# Architecture

Detailed product, installation, API, update, and Git checkpoint planning lives in [product-roadmap.md](product-roadmap.md).

## Milestones

### M1: Core protocol

- [x] WARP register + MASQUE enroll
- [x] SEC1 private key encoding
- [x] mihomo MASQUE YAML generation
- [x] Unit tests with an HTTP mock

### M2: Runtime

- [x] Start/stop mihomo with PID and log files
- [ ] Discover and validate the architecture-matching mihomo Alpha binary from the installation root
- [ ] Poll the mihomo controller API and verify IPv6 egress
- [ ] Crash cleanup and stale-process recovery

### M3: Windows networking

- [ ] Detect the physical uplink adapter and existing IPv6 default route
- [ ] Implement an explicit, reversible IPv4/IPv6 policy
- [ ] Add administrator helper or Windows service with an explicit lifecycle API
- [ ] Restore the pre-activation network snapshot on every exit path

### M4: Desktop distribution

- [ ] Add Tauri 2 shell and tray lifecycle to this repository
- [ ] Keep privileged operations in the Go helper
- [ ] Store credentials with Windows DPAPI/Credential Manager
- [ ] Download and verify the architecture-matching mihomo runtime
- [ ] CI builds for Windows x64, installer upgrades, and release artifacts

## Process boundary

The GUI must not own the WARP token, private key, or privileged network operations. The Go core owns those operations and exposes a narrow local IPC/API surface. For the first implementation an authenticated loopback HTTP API is acceptable; a named pipe is preferable before public release. The final product is built from `freev6`; the older BKNetwork repository is a reference for Tauri tray and lifecycle patterns, not a second runtime to embed.

## Configuration boundary

`warp.json` is private device state. `mihomo.yaml` is generated runtime configuration and contains the MASQUE private key, so it is also private. Neither file belongs in source control. The installed application resolves runtime paths from its installation root and stores user-writable state in dedicated state/config directories, never relative to the current working directory.
