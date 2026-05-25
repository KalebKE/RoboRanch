# RoboRanch

RoboRanch is a lightweight Android emulator and device lease broker for local developers, agentic coding sessions, and self-hosted CI runners.

It is intentionally smaller than Appium Grid, Selenium Grid, or a device cloud. Those tools route test protocols. RoboRanch manages the host-level pool underneath: which emulator is free, how long a job may hold it, whether it is healthy, and how to clean it before the next job.

## Status

This repo is the standalone extraction of a working private emulator-pool script. The private host paths, device serials, and deployment names have been removed. The first implementation is a Go CLI with macOS launchd and Linux systemd templates.

## Install

```sh
go install github.com/TracqiTechnology/roboranch/cmd/roboranch@latest
```

For local development:

```sh
go build -o bin/roboranch ./cmd/roboranch
```

## Quick Start

Create a config:

```sh
roboranch init
```

Edit `~/.config/roboranch/pool.json` with your emulator serials, labels, and optional launchd/systemd service names. A sanitized example is in [examples/pool.example.json](examples/pool.example.json).

Check the host:

```sh
roboranch doctor
roboranch list
```

Lease a device:

```sh
roboranch checkout --type emulator --label api36 --wait 10m --json
```

Run a test command with a leased device:

```sh
roboranch with-lease --type emulator --label api36 --wait 10m -- ./gradlew connectedDebugAndroidTest
```

`with-lease` exports:

- `ANDROID_SERIAL`
- `ROBORANCH_DEVICE_ID`
- `ROBORANCH_LEASE_ID`

The lease is cleaned and released after the child command exits.

## Commands

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

## Cleanup Policy

Emulators are cleaned by default on release:

- uninstall third-party packages
- force-stop packages before uninstall
- run `am kill-all`
- run `sync`
- clear logcat
- reset animation scales to `0.0`

RoboRanch intentionally does not call `pm trim-caches`; that command caused follow-on instrumentation installs to fail in the source pool.

Physical devices are not cleaned by default. Enable cleanup per physical device only when that is acceptable for the hardware:

```json
{
  "id": "physical-1",
  "type": "device",
  "serial": "REPLACE_WITH_USB_SERIAL",
  "cleanup": {"enabled": true}
}
```

## Runner Model

RoboRanch does not run a daemon in v1. `checkout --wait` polls atomic lease files with stale lease garbage collection. That is enough for concurrent local terminals, coding agents, and CI jobs on the same host.

For a long-lived pool, use launchd on macOS or systemd user services on Linux to keep emulators warm from clean snapshots. See [docs/quickstart-macos.md](docs/quickstart-macos.md), [docs/quickstart-linux.md](docs/quickstart-linux.md), and [docs/github-actions.md](docs/github-actions.md).

