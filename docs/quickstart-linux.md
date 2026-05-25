# Linux Quickstart

This path is for Linux workstations and self-hosted Linux GitHub Actions runners with Android emulator acceleration configured.

## 1. Create AVDs

Create one AVD per pool slot, boot each one, disable animations, and save a clean snapshot named `ci-clean`.

## 2. Install systemd user services

Copy [templates/systemd/roboranch-pool-N.service.tmpl](../templates/systemd/roboranch-pool-N.service.tmpl) to `~/.config/systemd/user/`, replacing:

- `{{N}}`
- `{{ANDROID_SDK}}`
- `{{PORT}}`
- `{{AVD_NAME}}`
- `{{STATE_DIR}}`

Enable and start each service:

```sh
systemctl --user daemon-reload
systemctl --user enable --now roboranch-pool-1.service
```

## 3. Configure RoboRanch

```sh
roboranch init
```

Set `systemdUnit` for each emulator in `~/.config/roboranch/pool.json`.

## 4. Verify

```sh
roboranch doctor
roboranch list
roboranch checkout --type emulator --wait 5m
```

