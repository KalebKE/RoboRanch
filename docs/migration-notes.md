# Migration Notes

RoboRanch was extracted from a private `emulator-pool` folder that used Bash scripts, `jq`, `shlock`, launchd plists, and a private `pool.json`.

The migrated behavior keeps:

- lease/release lifecycle
- TTL-based stale lease cleanup
- device filtering by type, serial, and labels
- emulator cleanup on release
- launchd repair for macOS
- clean-snapshot warm emulator model

The migrated behavior changes:

- private host paths are replaced by config defaults
- private serial numbers are replaced by examples
- lock and lease metadata are JSON
- queueing is explicit through `checkout --wait`
- Linux systemd templates are included beside macOS launchd templates
- `pm trim-caches` remains intentionally omitted

