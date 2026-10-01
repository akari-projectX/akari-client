# Packaging & release (M4 remainder)

`make dist` produces per-platform directories (client + kernel + license
files) and `SHA256SUMS`. Installers are **not implemented yet**; this is the
work list to finish M4.

## Common

- [ ] Version from tag: `make dist VERSION=vX.Y.Z` (ldflags inject version and
      commit; `akari-client version` prints both plus the kernel pin).
- [ ] GPL-3.0 corresponding source for the kernel: per release, archive
      upstream mihomo v1.19.32 source + `third_party/mihomo/` and publish it
      next to the installers (or ship a written offer). Reference it in the
      installer and About text.
- [ ] Ship `THIRD-PARTY-LICENSES.txt` and `mihomo-LICENSE.txt` with every
      installer.
- [ ] Release workflow (`.github/workflows/release.yml`, on tag): build all
      targets, sign, notarize, upload installers + SHA256SUMS + kernel source
      archive. Reproducibility check like akari-agent's.
- [ ] Auto-start at login (optional, off by default): Windows `Run` key,
      macOS LaunchAgent / `SMAppService`, Linux XDG autostart `.desktop`.
- [ ] Uninstall: revert the system proxy if it points at the client
      (`akari-client` does this on Quit; the uninstaller must handle a client
      that is not running), remove the config dir only on explicit request.

## Windows (first platform)

- [ ] NSIS (or WiX) per-user installer into `%LOCALAPPDATA%\Programs\Akari`
      with `akari-client.exe` + `mihomo.exe` side by side (the client looks
      for the kernel next to itself), Start-menu shortcut, uninstaller.
- [ ] Authenticode-sign both executables and the installer (EV certificate
      recommended to avoid SmartScreen warnings); timestamping.
- [ ] Kill running `akari-client.exe` (it stops its kernel via the job
      object) before upgrade.
- [ ] Manual test matrix (not automatable here): tray icon/menu on Windows 10
      and 11, system proxy on/off (Edge/Chrome follow WinINet), proxy reverted
      after Quit and after a forced kill + relaunch, sleep/resume and Wi-Fi
      switch self-heal, first-run settings page sign-in, single instance,
      Windows Defender / SmartScreen behaviour with signed binaries.

## macOS

- [ ] `.app` bundle: `Contents/MacOS/akari-client` and
      `Contents/MacOS/mihomo`, `LSUIElement=true` (tray only, no Dock icon).
- [ ] Universal or per-arch builds (CI builds arm64 + amd64 on macos-latest).
- [ ] Codesign with hardened runtime (both executables), notarize, staple;
      `.pkg` or `.dmg`.
- [ ] Verify `networksetup` proxy changes work without an admin prompt on
      current macOS for standard users; otherwise add a privileged helper.

## Linux

- [ ] AppImage (client + kernel in `usr/bin`), plus `.desktop` and icon.
- [ ] Document tray requirements: StatusNotifierItem host (KDE; GNOME needs
      the AppIndicator extension). System proxy: GNOME/KDE only.
