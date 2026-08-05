package roboranch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

type HostManager struct {
	runner CommandRunner
}

func newHostManager(runner CommandRunner) HostManager {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return HostManager{runner: runner}
}

func (h HostManager) repair(ctx context.Context, cfg Config, adb ADB, device DeviceConfig) error {
	if device.Type != DeviceTypeEmulator {
		return fmt.Errorf("%s is not an emulator", device.ID)
	}
	// Reachable AND usable. Guarding on adb.healthy alone made repair disagree with the
	// health assessment that sent work here: the CLI decided a wedged device was unhealthy,
	// printed "restarting", and this returned nil because adb could still reach it. Three
	// emulators sat wedged for 22 days of uptime while repair reported repaired=3.
	if adb.healthy(ctx, device) && !adb.wedged(ctx, device) {
		return nil
	}
	if err := h.restart(ctx, device); err != nil {
		return err
	}
	deadline := time.Now().Add(deviceBootTimeout(cfg, device))
	for time.Now().Before(deadline) {
		if adb.bootCompleted(ctx, device) || adb.healthy(ctx, device) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("%s did not boot before timeout", device.ID)
}

func (h HostManager) restart(ctx context.Context, device DeviceConfig) error {
	if runtime.GOOS == "darwin" && device.LaunchdLabel != "" {
		return h.restartLaunchd(ctx, device.LaunchdLabel)
	}
	if runtime.GOOS == "linux" && device.SystemdUnit != "" {
		_, err := h.runner.Run(ctx, "systemctl", "--user", "restart", device.SystemdUnit)
		return err
	}
	if device.LaunchdLabel != "" {
		return fmt.Errorf("launchd repair is available only on macOS")
	}
	if device.SystemdUnit != "" {
		return fmt.Errorf("systemd repair is available only on Linux")
	}
	return fmt.Errorf("%s has no launchdLabel or systemdUnit repair backend", device.ID)
}

func (h HostManager) restartLaunchd(ctx context.Context, label string) error {
	uid := strconv.Itoa(os.Getuid())
	if _, err := h.runner.Run(ctx, "launchctl", "kickstart", "-k", "gui/"+uid+"/"+label); err == nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	if _, statErr := os.Stat(plist); statErr != nil {
		return fmt.Errorf("launchd kickstart failed and plist is missing at %s", plist)
	}
	_, err = h.runner.Run(ctx, "launchctl", "bootstrap", "gui/"+uid, plist)
	return err
}
