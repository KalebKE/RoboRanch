# RoboRanch

RoboRanch is a lightweight Android emulator and device lease broker for local developers, agentic coding sessions, and CI runners.

It is intentionally smaller than Appium Grid, Selenium Grid, or a device cloud. Those tools route test protocols. RoboRanch manages the host-level pool underneath: which emulator or device is free, how long a job may hold it, whether it is healthy, and how to clean it before the next job.

## What It Does

RoboRanch gives multiple local terminals, coding agents, build scripts, and CI jobs a shared way to lease Android targets:

```sh
roboranch with-lease --type emulator --label api36 --wait 20m -- ./gradlew connectedDebugAndroidTest
```

While the command runs, RoboRanch exports:

- `ANDROID_SERIAL`
- `ROBORANCH_DEVICE_ID`
- `ROBORANCH_LEASE_ID`

When the command exits, RoboRanch cleans the emulator and releases the lease.

## Install

Install from source:

```sh
go install github.com/TracqiTechnology/roboranch/cmd/roboranch@latest
```

For local development on RoboRanch itself:

```sh
git clone https://github.com/TracqiTechnology/roboranch.git
cd roboranch
go test ./...
go build -o bin/roboranch ./cmd/roboranch
```

If you built locally, either add `./bin` to `PATH` or run `./bin/roboranch`.

## Prerequisites

Each host that runs RoboRanch needs:

- Android SDK platform tools, especially `adb`
- Android emulator binaries if using emulators
- at least one bootable AVD or attached physical device
- a shared RoboRanch config for all jobs on that host
- a shared RoboRanch state directory for locks and lease metadata

RoboRanch discovers the Android SDK from:

1. `ANDROID_HOME`
2. `ANDROID_SDK_ROOT`
3. common platform defaults, such as `~/Library/Android/sdk` on macOS and `~/Android/Sdk` on Linux

You can also set `androidSdk` directly in the config.

## Configuration

Create the default config:

```sh
roboranch init
```

By default this writes:

```text
~/.config/roboranch/pool.json
```

Override the config path with either:

```sh
roboranch --config ./pool.json list
```

or:

```sh
export ROBORANCH_CONFIG="$PWD/pool.json"
```

A complete sanitized example is in [examples/pool.example.json](examples/pool.example.json).

### Minimal Config

```json
{
  "version": 1,
  "stateDir": "~/.local/share/roboranch",
  "androidSdk": "",
  "defaultTTL": "30m",
  "repairTimeout": "2m",
  "devices": [
    {
      "id": "api36-1",
      "type": "emulator",
      "serial": "emulator-5554",
      "labels": ["emulator", "api36", "x86_64"]
    },
    {
      "id": "usb-1",
      "type": "device",
      "serial": "REPLACE_WITH_ADB_SERIAL",
      "labels": ["device", "physical"],
      "cleanup": {"enabled": false}
    }
  ]
}
```

Important fields:

- `stateDir`: shared lock, lease, and log directory for this host.
- `androidSdk`: optional SDK path. Leave empty to use environment/default discovery.
- `defaultTTL`: default lease lifetime. Expired leases are reaped by `gc` and before checkout.
- `repairTimeout`: how long emulator repair waits for boot.
- `devices[].id`: stable RoboRanch id used for leases.
- `devices[].type`: `emulator` or `device`.
- `devices[].serial`: ADB serial, such as `emulator-5554` or a USB device serial from `adb devices`.
- `devices[].labels`: selectors used by jobs, such as `api36`, `x86_64`, `pixel`, or `physical`.
- `devices[].cleanup.enabled`: physical device cleanup is disabled by default; emulator cleanup is enabled by default.
- `devices[].launchdLabel`: macOS service name used by `repair`.
- `devices[].systemdUnit`: Linux user service name used by `repair`.

Check the config and host:

```sh
roboranch doctor
roboranch list
roboranch list --json
```

## Local Development Setup

Use this mode when a developer machine has one or more already-running emulators or USB devices.

1. Start an emulator with Android Studio, `emulator`, or your usual Android tooling.

2. Find its serial:

```sh
adb devices
```

3. Put that serial in `~/.config/roboranch/pool.json`.

4. Verify RoboRanch can see it:

```sh
roboranch list
```

5. Run local tests through a lease:

```sh
roboranch with-lease --type emulator --wait 5m -- ./gradlew connectedDebugAndroidTest
```

For multiple concurrent coding agents or terminals, make sure they all use the same `ROBORANCH_CONFIG` and `stateDir`. They can then share the pool without colliding:

```sh
export ROBORANCH_CONFIG="$HOME/.config/roboranch/pool.json"
roboranch with-lease --label api36 --wait 20m -- ./gradlew connectedDebugAndroidTest
```

Manual checkout and release are also supported:

```sh
lease_info=$(roboranch checkout --type emulator --label api36 --wait 10m)
lease=$(printf '%s' "$lease_info" | awk '{print $1}')
serial=$(printf '%s' "$lease_info" | awk '{print $2}')
id=$(printf '%s' "$lease_info" | awk '{print $3}')

ANDROID_SERIAL="$serial" ./gradlew connectedDebugAndroidTest
roboranch release --id "$id" --lease "$lease"
```

Prefer `with-lease` for normal use because it releases automatically.

## Warm Local Emulator Pool

For faster repeated tests, keep emulators running from a clean snapshot and let RoboRanch lease them.

Recommended pool shape:

- one AVD per slot
- one fixed port per AVD
- one clean snapshot named `ci-clean`
- one launchd or systemd service per AVD
- one matching device entry in `pool.json`

Example serial mapping:

```text
pool-1 -> emulator-5554
pool-2 -> emulator-5556
pool-3 -> emulator-5558
pool-4 -> emulator-5560
```

### macOS launchd

Use [templates/launchd/com.roboranch.pool-N.plist.tmpl](templates/launchd/com.roboranch.pool-N.plist.tmpl).

Replace:

- `{{N}}`
- `{{ANDROID_SDK}}`
- `{{PORT}}`
- `{{AVD_NAME}}`
- `{{STATE_DIR}}`
- `{{HOME}}`

Install and start:

```sh
mkdir -p "$HOME/Library/LaunchAgents"
cp com.roboranch.pool-1.plist "$HOME/Library/LaunchAgents/"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.roboranch.pool-1.plist"
launchctl kickstart -k "gui/$(id -u)/com.roboranch.pool-1"
```

Configure the matching device with `launchdLabel`:

```json
{
  "id": "pool-1",
  "type": "emulator",
  "serial": "emulator-5554",
  "launchdLabel": "com.roboranch.pool-1",
  "labels": ["emulator", "api36", "x86_64"]
}
```

### Linux systemd

Use [templates/systemd/roboranch-pool-N.service.tmpl](templates/systemd/roboranch-pool-N.service.tmpl).

Replace:

- `{{N}}`
- `{{ANDROID_SDK}}`
- `{{PORT}}`
- `{{AVD_NAME}}`
- `{{STATE_DIR}}`

Install and start:

```sh
mkdir -p "$HOME/.config/systemd/user"
cp roboranch-pool-1.service "$HOME/.config/systemd/user/"
systemctl --user daemon-reload
systemctl --user enable --now roboranch-pool-1.service
```

Configure the matching device with `systemdUnit`:

```json
{
  "id": "pool-1",
  "type": "emulator",
  "serial": "emulator-5554",
  "systemdUnit": "roboranch-pool-1.service",
  "labels": ["emulator", "api36", "x86_64"]
}
```

## Remote Runner Setup

RoboRanch is most useful on self-hosted runners because the emulator pool can stay warm across jobs. It can also wrap a single emulator on an ephemeral hosted runner, but hosted runners do not get the same warm-pool benefit.

### Self-hosted GitHub Actions Runner

On the runner host:

1. Install Android SDK, platform tools, emulator, and system images.
2. Create AVDs and save clean snapshots.
3. Install launchd agents on macOS or systemd user services on Linux.
4. Install `roboranch`.
5. Create a shared config, for example `/opt/roboranch/pool.json` or `$HOME/.config/roboranch/pool.json`.
6. Use a persistent `stateDir`, for example `/opt/roboranch/state` or `$HOME/.local/share/roboranch`.
7. Export `ROBORANCH_CONFIG` in the runner service environment.

Example workflow:

```yaml
name: Android Instrumented Tests

on:
  pull_request:

jobs:
  connected-tests:
    runs-on: [self-hosted, macOS, android]
    steps:
      - uses: actions/checkout@v4
      - name: Check RoboRanch
        run: roboranch list
      - name: Run connected tests
        run: roboranch with-lease --type emulator --label api36 --wait 20m -- ./gradlew connectedDebugAndroidTest
```

For Linux self-hosted runners, use runner labels that match your fleet:

```yaml
runs-on: [self-hosted, Linux, android]
```

The important part is that all concurrent jobs on the same runner host share the same `ROBORANCH_CONFIG` and `stateDir`.

### GitHub-hosted Runner

Use this mode when the workflow creates and boots one emulator inside the job. RoboRanch will still provide consistent lease and cleanup behavior, but it will not provide a pre-warmed pool across jobs.

Example job-local config:

```yaml
- name: Write RoboRanch config
  run: |
    mkdir -p "$RUNNER_TEMP/roboranch"
    cat > "$RUNNER_TEMP/roboranch/pool.json" <<JSON
    {
      "version": 1,
      "stateDir": "$RUNNER_TEMP/roboranch/state",
      "defaultTTL": "30m",
      "devices": [
        {
          "id": "hosted-emulator",
          "type": "emulator",
          "serial": "emulator-5554",
          "labels": ["emulator", "hosted"]
        }
      ]
    }
    JSON
    echo "ROBORANCH_CONFIG=$RUNNER_TEMP/roboranch/pool.json" >> "$GITHUB_ENV"
```

After your workflow boots the emulator:

```yaml
- name: Run connected tests
  run: roboranch with-lease --type emulator --wait 5m -- ./gradlew connectedDebugAndroidTest
```

If your emulator serial differs, generate the config after `adb devices` reports the emulator.

## Cleanup Policy

Emulators are cleaned by default on release:

- uninstall third-party packages
- force-stop packages before uninstall
- run `am kill-all`
- run `sync`
- clear logcat
- reset animation scales to `0.0`

RoboRanch intentionally does not call `pm trim-caches`; that command caused follow-on instrumentation installs to fail in the source pool.

Physical devices are not cleaned by default. Enable cleanup per physical device only when that is acceptable for that hardware:

```json
{
  "id": "physical-1",
  "type": "device",
  "serial": "REPLACE_WITH_ADB_SERIAL",
  "cleanup": {"enabled": true}
}
```

## Command Reference

```text
roboranch init [--force]
roboranch doctor
roboranch list [--json]
roboranch status --id ID [--json]
roboranch checkout [--type emulator|device|any] [--label LABEL] [--serial SERIAL] [--ttl DURATION] [--wait DURATION] [--json]
roboranch release --id ID [--lease LEASE]
roboranch with-lease [checkout selectors] -- CMD [ARGS...]
roboranch repair --id ID|--all
roboranch gc [--verbose]
```

Exit codes:

- `0`: success
- `1`: matching devices are currently unavailable
- `2`: bad arguments or no selector match
- `3`: matching devices are unhealthy

## Development

Run the checks:

```sh
go test ./...
go vet ./...
```

Build local release targets:

```sh
GOOS=darwin GOARCH=arm64 go build -o dist/roboranch-darwin-arm64 ./cmd/roboranch
GOOS=linux GOARCH=amd64 go build -o dist/roboranch-linux-amd64 ./cmd/roboranch
```
