# freev6 Tauri shell

The initial Tauri 2 shell loads the Material You interface from `../web`, provides a Windows tray menu, and packages with the NSIS target. On Windows it requests administrator permission at launch; `freev6-helper` runs as a sidecar in the same elevated context and receives the installation root through `FREEV6_ROOT`.

```powershell
cargo check --manifest-path src-tauri/Cargo.toml
cargo tauri dev
cargo tauri build
```

The frontend reads `/api/v1/status`, `/api/v1/runtime`, and `/api/v1/settings` from the local helper. Proxy start/stop, WARP registration, and verified mihomo Alpha download are also available locally for testing. These action endpoints still need per-launch authentication before public release. The GUI must not own WARP credentials, mihomo lifecycle, or privileged network operations; those remain in the Go helper.
