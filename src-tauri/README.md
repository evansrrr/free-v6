# freev6 Tauri shell

The initial Tauri 2 shell loads the Material You interface from `../web`, provides a Windows tray menu, and packages with the NSIS target. It starts `freev6-helper` as a sidecar and passes the installation root through `FREEV6_ROOT`.

```powershell
cargo check --manifest-path src-tauri/Cargo.toml
cargo tauri dev
cargo tauri build
```

The frontend reads `/api/v1/status`, `/api/v1/runtime`, and `/api/v1/settings` from the local helper. Proxy start/stop and WARP registration will be added to the same API. The GUI must not own WARP credentials, mihomo lifecycle, or privileged network operations; those remain in the Go helper.
