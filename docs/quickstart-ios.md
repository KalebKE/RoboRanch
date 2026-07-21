# iOS Quickstart

This path is for a local Mac or self-hosted macOS CI runner. RoboRanch uses Xcode's `simctl` for simulators and `devicectl` for physical iPhones.

## 1. Verify Xcode

Select a full Xcode installation and confirm the tools are available:

```sh
xcode-select -p
xcodebuild -version
xcrun --find simctl
xcrun --find devicectl
```

## 2. Create a Fixed Simulator Pool

List installed runtimes and simulator device types:

```sh
xcrun simctl list runtimes available
xcrun simctl list devicetypes
```

Create one simulator per slot. Use identifiers reported by the two commands above:

```sh
xcrun simctl create "Pool-iPhone-1" \
  com.apple.CoreSimulator.SimDeviceType.iPhone-16e \
  com.apple.CoreSimulator.SimRuntime.iOS-26-4
```

Repeat for each desired slot, then capture the UDIDs:

```sh
xcrun simctl list devices available
```

RoboRanch boots a shutdown simulator during checkout. On release it shuts the simulator down, erases all contents and settings, boots it, waits for readiness, and only then unlocks the slot.

## 3. Configure the Simulators

Add the fixed UDIDs to `~/.config/roboranch/pool.json`:

```json
{
  "version": 1,
  "stateDir": "~/.local/share/roboranch",
  "defaultTTL": "30m",
  "repairTimeout": "2m",
  "devices": [
    {
      "id": "ios-sim-1",
      "platform": "ios",
      "type": "simulator",
      "serial": "REPLACE_WITH_SIMULATOR_UDID",
      "labels": ["simulator", "ios26", "iphone", "pool-1"]
    }
  ]
}
```

Check the host and pool:

```sh
roboranch doctor
roboranch list
roboranch status --id ios-sim-1
```

## 4. Run iOS Tests

RoboRanch exports the leased UDID and a complete Xcode destination:

```sh
roboranch with-lease --platform ios --type simulator --label ios26 --wait 20m -- \
  sh -c 'xcodebuild test -scheme MyApp -destination "$ROBORANCH_XCODE_DESTINATION"'
```

The shell wrapper is needed because the destination variable is created for the leased child process.

Manual checkout is also supported:

```sh
lease_info=$(roboranch checkout --platform ios --type simulator --label ios26 --wait 10m)
lease=$(printf '%s' "$lease_info" | awk '{print $1}')
udid=$(printf '%s' "$lease_info" | awk '{print $2}')
id=$(printf '%s' "$lease_info" | awk '{print $3}')

xcodebuild test -scheme MyApp -destination "platform=iOS Simulator,id=$udid"
roboranch release --id "$id" --lease "$lease"
```

## 5. Add a Physical iPhone

Before adding a phone, pair it with Xcode, enable Developer Mode, connect it to developer services, and keep it unlocked. Find its UDID with:

```sh
xcrun devicectl list devices
```

Configure it without cleanup:

```json
{
  "id": "iphone-1",
  "platform": "ios",
  "type": "device",
  "serial": "REPLACE_WITH_IPHONE_UDID",
  "labels": ["device", "ios", "iphone", "physical"],
  "cleanup": {"enabled": false}
}
```

Use it with Xcode:

```sh
roboranch with-lease --platform ios --type device --wait 10m -- \
  sh -c 'xcodebuild test -scheme MyApp -destination "$ROBORANCH_XCODE_DESTINATION"'
```

RoboRanch only health-checks and leases physical iPhones. It never pairs, unlocks, registers, repairs, erases, or removes apps from them.

## Self-hosted GitHub Actions

All jobs on the Mac must share the same config and state directory:

```yaml
jobs:
  ios-tests:
    runs-on: [self-hosted, macOS, ios]
    steps:
      - uses: actions/checkout@v4
      - name: Check RoboRanch
        run: roboranch doctor
      - name: Run tests
        run: |
          roboranch with-lease --platform ios --type simulator --label ios26 --wait 20m -- \
            sh -c 'xcodebuild test -scheme MyApp -destination "$ROBORANCH_XCODE_DESTINATION"'
```
