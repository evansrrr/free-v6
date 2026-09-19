# Architecture

## Milestones

### M1: Core protocol

- [x] WARP register + MASQUE enroll
- [x] SEC1 private key encoding
- [x] mihomo MASQUE YAML generation
- [x] Unit tests with an HTTP mock

### M2: Runtime

- [ ] Discover and validate mihomo binary
- [ ] Start/stop mihomo with an isolated working directory
- [ ] Poll the mihomo controller API and verify IPv6 egress
- [ ] Crash cleanup and stale-process recovery

### M3: Windows networking

- [ ] Detect the physical uplink adapter and existing IPv6 default route
- [ ] Implement an explicit, reversible IPv4/IPv6 policy
- [ ] Add administrator helper or Windows service
- [ ] Restore the pre-activation network snapshot on every exit path

### M4: Desktop distribution

- [ ] Add Tauri 2 shell and tray lifecycle
- [ ] Keep privileged operations in the Go helper
- [ ] Store credentials with Windows DPAPI/Credential Manager
- [ ] CI builds for Windows x64 and signed release artifacts

## Process boundary

The GUI must not own the WARP token, private key, or privileged network operations. The Go core owns those operations and exposes a narrow local IPC/API surface. For the first implementation an authenticated loopback HTTP API is acceptable; a named pipe is preferable before public release.

## Configuration boundary

`warp.json` is private device state. `mihomo.yaml` is generated runtime configuration and contains the MASQUE private key, so it is also private. Neither file belongs in source control.
