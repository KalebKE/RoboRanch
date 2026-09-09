package roboranch

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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

// wedged reports whether an ANR dialog owns the screen.
//
// `adb get-state` keeps answering "device" long after the UI is unusable: a system_server
// ANR stalls broadcast delivery and covers whatever is under it, so a lease handed out in
// that state fails in ways that look like the caller's fault -- a broadcast that never
// answers reads as a timeout, and an assertion under the dialog reads as stale UI.
//
// Best-effort: a device that will not answer dumpsys is not declared wedged on that basis
// alone. `healthy` already covers unreachable.
func (a ADB) wedged(ctx context.Context, device DeviceConfig) bool {
	out, err := a.run(ctx, device.Serial, "shell", "dumpsys", "window")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "mCurrentFocus") {
			continue
		}
		return strings.Contains(line, "Application Not Responding")
	}
	return false
}

// clockSkew reports how far the device's clock sits from this host, and whether it could
// be read at all.
//
// A pooled emulator is resumed rather than recreated, so it can come back with a clock
// months behind. Nothing about it looks broken -- adb answers "device", the screen is
// idle -- but every TLS chain fails notBefore validation, so the net stack reports
// ERR_CERT_DATE_INVALID, Conscrypt raises "Chain validation failed", and Firebase wraps
// that as "An internal error has occurred". The failure surfaces as an auth bug in
// whatever happens to sign in first, on branches that touch no auth code.
//
// Best-effort, like wedged: a device that will not answer is not condemned on that basis.
func (a ADB) clockSkew(ctx context.Context, device DeviceConfig) (time.Duration, bool) {
	out, err := a.run(ctx, device.Serial, "shell", "date", "-u", "+%s")
	if err != nil {
		return 0, false
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(strings.ReplaceAll(out, "\r", "")), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Since(time.Unix(epoch, 0)), true
}

func (a ADB) bootCompleted(ctx context.Context, device DeviceConfig) bool {
	out, err := a.run(ctx, device.Serial, "shell", "getprop", "sys.boot_completed")
	return err == nil && strings.TrimSpace(strings.ReplaceAll(out, "\r", "")) == "1"
}

func (a ADB) cleanup(ctx context.Context, device DeviceConfig, stderr io.Writer) error {
	if !device.cleanupEnabled() {
		return nil
	}
	fmt.Fprintf(stderr, "roboranch: cleaning up %s (%s)\n", device.ID, device.Serial)
	out, err := a.run(ctx, device.Serial, "shell", "pm", "list", "packages", "-3")
	if err != nil {
		return fmt.Errorf("list third-party packages on %s: %w", device.ID, err)
	}
	for _, line := range strings.Split(out, "\n") {
		pkg := strings.TrimSpace(strings.TrimPrefix(line, "package:"))
		if pkg == "" {
			continue
		}
		_, _ = a.run(ctx, device.Serial, "shell", "am", "force-stop", pkg)
		if out, err := a.run(ctx, device.Serial, "uninstall", pkg); err != nil || strings.Contains(out, "Failure") {
			return fmt.Errorf("uninstall %s on %s failed: %s (%v)", pkg, device.ID, strings.TrimSpace(out), err)
		}
	}
	_, _ = a.run(ctx, device.Serial, "shell", "sync")
	// Keep crash evidence available after release; consumers capture logcat
	// continuously and select their own timestamps instead of clearing the ring.
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "window_animation_scale", "0.0")
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "transition_animation_scale", "0.0")
	_, _ = a.run(ctx, device.Serial, "shell", "settings", "put", "global", "animator_duration_scale", "0.0")
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "roboranch: cleanup done for %s\n", device.ID)
	return nil
}
