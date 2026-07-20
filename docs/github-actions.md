# GitHub Actions

RoboRanch is most useful on self-hosted runners where Android emulators and iOS simulators stay warm across jobs.

## Self-hosted Android runner

```yaml
name: Android Instrumented Tests

on: [pull_request]

jobs:
  connected-tests:
    runs-on: [self-hosted, macOS, android]
    steps:
      - uses: actions/checkout@v4
      - name: Run connected tests
        run: roboranch with-lease --type emulator --label api36 --wait 20m -- ./gradlew connectedDebugAndroidTest
```

## Hosted runner

Hosted runners are ephemeral, so a warm pool is less valuable. You can still use RoboRanch as a single-job lease wrapper after your workflow creates and boots an emulator:

```yaml
- name: Run connected tests
  run: roboranch with-lease --type emulator --wait 2m -- ./gradlew connectedDebugAndroidTest
```

Use a job-local config with `ROBORANCH_CONFIG` when the hosted runner creates a temporary emulator serial.

## Self-hosted iOS runner

iOS requires a macOS runner with Xcode and fixed simulator UDIDs configured on that host:

```yaml
name: iOS Tests

on: [pull_request]

jobs:
  ios-tests:
    runs-on: [self-hosted, macOS, ios]
    steps:
      - uses: actions/checkout@v4
      - name: Run tests
        run: |
          roboranch with-lease --platform ios --type simulator --label ios26 --wait 20m -- \
            sh -c 'xcodebuild test -scheme MyApp -destination "$ROBORANCH_XCODE_DESTINATION"'
```

Physical iPhones can use `--platform ios --type device`, but the runner must keep each phone paired, connected to developer services, unlocked, and in Developer Mode.
