package roboranch

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type ADB struct {
	path   string
	runner CommandRunner
}

func newADB(path string, runner CommandRunner) ADB {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return ADB{path: path, runner: runner}
}

func (a ADB) run(ctx context.Context, serial string, args ...string) (string, error) {
	fullArgs := append([]string{"-s", serial}, args...)
	return a.runner.Run(ctx, a.path, fullArgs...)
}

func (a ADB) healthy(ctx context.Context, device DeviceConfig) bool {
	out, err := a.run(ctx, device.Serial, "get-state")
	return err == nil && strings.TrimSpace(out) == "device"
}

func (a ADB) bootCompleted(ctx context.Context, device DeviceConfig) bool {
	out, err := a.run(ctx, device.Serial, "shell", "getprop", "sys.boot_completed")
	return err == nil && strings.TrimSpace(strings.ReplaceAll(out, "\r", "")) == "1"
}

func (a ADB) cleanup(ctx context.Context, device DeviceConfig, stderr io.Writer) {
	if !device.cleanupEnabled() {
		return
	}
	fmt.Fprintf(stderr, "roboranch: cleaning up %s (%s)\n", device.ID, device.Serial)
	out, err := a.run(ctx, device.Serial, "shell", "pm", "list", "packages", "-3")
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			pkg := strings.TrimSpace(strings.TrimPrefix(line, "package:"))
			if pkg == "" {
				continue
			}
			_, _ = a.run(ctx, device.Serial, "shell", "am", "force-stop", pkg)
			_, _ = a.run(ctx, device.Serial, "uninstall", pkg)
		}
	}
	_, _ = a.run(ctx, device.Serial, "shell", "am", "kill-all")
	_, _ = a.run(ctx, device.Serial, "shell", "sync")
	_, _ = a.run(ctx, device.Serial, "logcat", "-c")
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "window_animation_scale", "0.0")
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "transition_animation_scale", "0.0")
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "animator_duration_scale", "0.0")
	fmt.Fprintf(stderr, "roboranch: cleanup done for %s\n", device.ID)
}
