# GitHub Actions

RoboRanch is most useful on self-hosted runners where emulators stay warm across jobs.

## Self-hosted runner

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

