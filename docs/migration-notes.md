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

## iOS support

Config version 1 remains valid. Existing devices with no `platform` field default to `android`, and checkout defaults to `--platform android`, so existing scripts do not begin leasing iOS targets.

iOS adds:

- fixed simulator pools addressed by UDID through `xcrun simctl`
- strict erase-and-warm-boot cleanup for simulators
- non-mutating physical iPhone leasing through `xcrun devicectl`
- `--platform ios` and the `simulator` target type
- `ROBORANCH_IOS_UDID` and `ROBORANCH_XCODE_DESTINATION` child-process variables
