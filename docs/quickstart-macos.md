# macOS Quickstart

This path is for a local Mac or a self-hosted macOS GitHub Actions runner.

## 1. Create AVDs

Create one AVD per pool slot with Android Studio, `avdmanager`, or another provisioning tool. Use unique ports:

```text
pool-1 -> emulator-5554
pool-2 -> emulator-5556
pool-3 -> emulator-5558
pool-4 -> emulator-5560
```

Boot each AVD once, disable animations, then save a clean snapshot named `ci-clean`.

## 2. Install launchd agents

Copy [templates/launchd/com.roboranch.pool-N.plist.tmpl](../templates/launchd/com.roboranch.pool-N.plist.tmpl) to `~/Library/LaunchAgents/`, replacing:

- `{{N}}`
- `{{ANDROID_SDK}}`
- `{{PORT}}`
- `{{AVD_NAME}}`
- `{{STATE_DIR}}`
- `{{HOME}}`

Load the agents:

```sh
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.roboranch.pool-1.plist
launchctl kickstart -k "gui/$(id -u)/com.roboranch.pool-1"
```

## 3. Configure RoboRanch

```sh
roboranch init
```

Set `launchdLabel` for each emulator in `~/.config/roboranch/pool.json`.

## 4. Verify

```sh
roboranch doctor
roboranch list
roboranch with-lease --type emulator --wait 5m -- adb devices
```

