package roboranch

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type App struct {
	runner CommandRunner
	stdout io.Writer
	stderr io.Writer
	config string
}

type runtimeState struct {
	config   Config
	path     string
	store    *LeaseStore
	backends backendRegistry
}

type checkoutOptions struct {
	platform   Platform
	deviceType DeviceType
	serial     string
	labels     []string
	ttl        time.Duration
	wait       time.Duration
	holderPID  int
	json       bool
}

type commandError struct {
	code int
	err  error
}

func (e commandError) Error() string {
	return e.err.Error()
}

func Execute(args []string, stdout, stderr io.Writer) int {
	app := &App{
		runner: execCommandRunner{},
		stdout: stdout,
		stderr: stderr,
	}
	return app.execute(args)
}

func (a *App) execute(args []string) int {
	remaining, code := a.parseGlobal(args)
	if code != exitOK {
		return code
	}
	if len(remaining) == 0 {
		a.usage()
		return exitUsage
	}
	command := remaining[0]
	commandArgs := remaining[1:]
	var err error
	switch command {
	case "help", "-h", "--help":
		a.usage()
		return exitOK
	case "init":
		err = a.cmdInit(commandArgs)
	case "doctor":
		err = a.cmdDoctor(commandArgs)
	case "list":
		err = a.cmdList(commandArgs)
	case "status":
		err = a.cmdStatus(commandArgs)
	case "checkout":
		err = a.cmdCheckout(commandArgs)
	case "release":
		err = a.cmdRelease(commandArgs)
	case "with-lease":
		err = a.cmdWithLease(commandArgs)
	case "repair":
		err = a.cmdRepair(commandArgs)
	case "gc":
		err = a.cmdGC(commandArgs)
	default:
		fmt.Fprintf(a.stderr, "roboranch: unknown command %q\n", command)
		a.usage()
		return exitUsage
	}
	if err == nil {
		return exitOK
	}
	var cmdErr commandError
	if errors.As(err, &cmdErr) {
		fmt.Fprintf(a.stderr, "roboranch: %s\n", cmdErr.err)
		return cmdErr.code
	}
	fmt.Fprintf(a.stderr, "roboranch: %s\n", err)
	return exitUsage
}

func (a *App) parseGlobal(args []string) ([]string, int) {
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(a.stderr, "roboranch: --config requires a path")
				return nil, exitUsage
			}
			a.config = args[i+1]
			i++
		default:
			remaining = append(remaining, args[i:]...)
			return remaining, exitOK
		}
	}
	return remaining, exitOK
}

func (a *App) usage() {
	fmt.Fprintln(a.stderr, `Usage: roboranch [--config PATH] <command> [args...]

Commands:
  init [--force]                         Write an example config
  doctor                                 Check config, state, and platform tools
  list [--json]                          List configured devices
  status --id ID [--json]                Show one device
  checkout [selectors] [--json]          Lease a device
  release --id ID [--lease LEASE]        Cleanup and release a device
  with-lease [selectors] -- CMD [ARGS...] Run a command with a leased device
  repair --id ID|--all                   Restart unhealthy virtual devices
  gc [--verbose]                         Reap stale or expired leases

Selectors:
  --platform android|ios|any             Defaults to android
  --type emulator|simulator|device|any
  --serial SERIAL_OR_UDID
  --label LABEL                          May be repeated
  --ttl DURATION                         30m, 1800, 45s
  --wait DURATION                        Wait for a free device before failing

Exit codes: 0 success, 1 unavailable, 2 bad args/no matches, 3 unhealthy.`)
}

func (a *App) load() (*runtimeState, error) {
	cfg, path, err := LoadConfig(a.config)
	if err != nil {
		return nil, err
	}
	store, err := newLeaseStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	return &runtimeState{
		config:   cfg,
		path:     path,
		store:    store,
		backends: newBackendRegistry(cfg, a.runner),
	}, nil
}

func (a *App) cmdInit(args []string) error {
	fs := newFlagSet("init", a.stderr)
	force := fs.Bool("force", false, "overwrite existing config")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	path := defaultConfigPath()
	if a.config != "" {
		path = expandPath(a.config)
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return commandError{code: exitUsage, err: fmt.Errorf("%s already exists; pass --force to overwrite", path)}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(exampleConfig()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s\n", path)
	return nil
}

func (a *App) cmdDoctor(args []string) error {
	fs := newFlagSet("doctor", a.stderr)
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	cfg, path, err := LoadConfig(a.config)
	if err != nil {
		fmt.Fprintf(a.stdout, "config: missing or invalid (%s)\n", path)
		fmt.Fprintf(a.stdout, "error: %s\n", err)
		return commandError{code: exitUnavailable, err: fmt.Errorf("doctor failed")}
	}
	fmt.Fprintf(a.stdout, "config: %s\n", path)
	fmt.Fprintf(a.stdout, "stateDir: %s\n", cfg.StateDir)
	fmt.Fprintf(a.stdout, "devices: %d\n", len(cfg.Devices))
	failed := false
	if cfg.hasPlatform(PlatformAndroid) {
		adbPath := cfg.adbPath()
		if _, err := os.Stat(adbPath); err != nil {
			if _, lookErr := exec.LookPath(adbPath); lookErr != nil {
				fmt.Fprintf(a.stdout, "adb: missing (%s)\n", adbPath)
				failed = true
			}
		}
		if !failed {
			if out, err := a.runner.Run(context.Background(), adbPath, "version"); err == nil {
				firstLine := strings.Split(strings.TrimSpace(out), "\n")[0]
				fmt.Fprintf(a.stdout, "adb: %s\n", firstLine)
			} else {
				fmt.Fprintf(a.stdout, "adb: found at %s, but version check failed: %s\n", adbPath, err)
				failed = true
			}
		}
	}
	if cfg.hasPlatform(PlatformIOS) {
		if runtime.GOOS != "darwin" {
			fmt.Fprintln(a.stdout, "ios: unavailable (iOS targets require macOS)")
			failed = true
		} else {
			if out, err := a.runner.Run(context.Background(), "xcrun", "--find", "simctl"); err == nil {
				fmt.Fprintf(a.stdout, "simctl: %s\n", strings.TrimSpace(out))
			} else {
				fmt.Fprintf(a.stdout, "simctl: missing (%s)\n", err)
				failed = true
			}
			if cfg.hasDevice(PlatformIOS, DeviceTypePhysical) {
				if out, err := a.runner.Run(context.Background(), "xcrun", "--find", "devicectl"); err == nil {
					fmt.Fprintf(a.stdout, "devicectl: %s\n", strings.TrimSpace(out))
				} else {
					fmt.Fprintf(a.stdout, "devicectl: missing (%s)\n", err)
					failed = true
				}
			}
		}
	}
	if failed {
		return commandError{code: exitUnavailable, err: fmt.Errorf("doctor failed")}
	}
	fmt.Fprintln(a.stdout, "doctor: ok")
	return nil
}

func (a *App) cmdList(args []string) error {
	fs := newFlagSet("list", a.stderr)
	jsonMode := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	statuses := a.statuses(context.Background(), rt)
	if *jsonMode {
		return json.NewEncoder(a.stdout).Encode(map[string]any{"devices": statuses})
	}
	fmt.Fprintf(a.stdout, "%-16s %-8s %-9s %-36s %-8s %-8s %-24s %s\n", "ID", "PLATFORM", "TYPE", "SERIAL/UDID", "LOCKED", "HEALTHY", "HOLDER", "REASON")
	for _, status := range statuses {
		holder := ""
		if status.Lease != nil {
			holder = fmt.Sprintf("%s:pid=%d", status.Lease.Hostname, status.Lease.HolderPID)
		}
		fmt.Fprintf(a.stdout, "%-16s %-8s %-9s %-36s %-8s %-8s %-24s %s\n",
			status.ID, status.Platform, status.Type, status.Serial, yesNo(status.Locked), yesNo(status.Healthy), holder, status.HealthReason)
	}
	return nil
}

func (a *App) cmdStatus(args []string) error {
	fs := newFlagSet("status", a.stderr)
	id := fs.String("id", "", "device id")
	jsonMode := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	if *id == "" {
		return commandError{code: exitUsage, err: fmt.Errorf("status requires --id")}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	device, ok := findDevice(rt.config.Devices, *id)
	if !ok {
		return commandError{code: exitUsage, err: fmt.Errorf("unknown device id %q", *id)}
	}
	status := a.status(context.Background(), rt, device)
	if *jsonMode {
		return json.NewEncoder(a.stdout).Encode(status)
	}
	fmt.Fprintf(a.stdout, "id=%s\n", status.ID)
	fmt.Fprintf(a.stdout, "platform=%s\n", status.Platform)
	fmt.Fprintf(a.stdout, "type=%s\n", status.Type)
	fmt.Fprintf(a.stdout, "serial=%s\n", status.Serial)
	fmt.Fprintf(a.stdout, "locked=%s\n", yesNo(status.Locked))
	if status.Lease != nil {
		fmt.Fprintf(a.stdout, "lease=%s\n", status.Lease.LeaseID)
		fmt.Fprintf(a.stdout, "holder_pid=%d\n", status.Lease.HolderPID)
		fmt.Fprintf(a.stdout, "hostname=%s\n", status.Lease.Hostname)
		fmt.Fprintf(a.stdout, "acquired_at=%s\n", status.Lease.AcquiredAt.Format(time.RFC3339))
		fmt.Fprintf(a.stdout, "expires_at=%s\n", status.Lease.ExpiresAt.Format(time.RFC3339))
	}
	if status.HolderAlive != nil {
		fmt.Fprintf(a.stdout, "holder_alive=%s\n", yesNo(*status.HolderAlive))
	}
	fmt.Fprintf(a.stdout, "healthy=%s\n", yesNo(status.Healthy))
	if status.HealthReason != "" {
		fmt.Fprintf(a.stdout, "health_reason=%s\n", status.HealthReason)
	}
	return nil
}

func (a *App) cmdCheckout(args []string) error {
	rt, err := a.load()
	if err != nil {
		return err
	}
	options, err := parseCheckoutOptions(args, rt.config.defaultTTLDuration(), false, a.stderr)
	if err != nil {
		return commandError{code: exitUsage, err: err}
	}
	result, code, err := a.checkout(context.Background(), rt, options)
	if err != nil {
		return commandError{code: code, err: err}
	}
	if options.json {
		return json.NewEncoder(a.stdout).Encode(result)
	}
	fmt.Fprintf(a.stdout, "%s\t%s\t%s\n", result.Lease, result.Serial, result.ID)
	return nil
}

func (a *App) cmdRelease(args []string) error {
	fs := newFlagSet("release", a.stderr)
	id := fs.String("id", "", "device id")
	lease := fs.String("lease", "", "expected lease id")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	if *id == "" {
		return commandError{code: exitUsage, err: fmt.Errorf("release requires --id")}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	device, ok := findDevice(rt.config.Devices, *id)
	if !ok {
		return commandError{code: exitUsage, err: fmt.Errorf("unknown device id %q", *id)}
	}
	if err := a.releaseDevice(context.Background(), rt, device, *lease); err != nil {
		return err
	}
	return nil
}

func (a *App) cmdWithLease(args []string) error {
	selectorArgs, commandArgs, ok := splitCommand(args)
	if !ok || len(commandArgs) == 0 {
		return commandError{code: exitUsage, err: fmt.Errorf("with-lease requires -- CMD [ARGS...]")}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	options, err := parseCheckoutOptions(selectorArgs, rt.config.defaultTTLDuration(), true, a.stderr)
	if err != nil {
		return commandError{code: exitUsage, err: err}
	}
	options.holderPID = os.Getpid()
	result, code, err := a.checkout(context.Background(), rt, options)
	if err != nil {
		return commandError{code: code, err: err}
	}
	device, _ := findDevice(rt.config.Devices, result.ID)
	cmd := exec.Command(commandArgs[0], commandArgs[1:]...)
	cmd.Stdout = a.stdout
	cmd.Stderr = a.stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(environmentWithout(os.Environ(),
		"ANDROID_SERIAL",
		"ROBORANCH_DEVICE_ID",
		"ROBORANCH_LEASE_ID",
		"ROBORANCH_PLATFORM",
		"ROBORANCH_DEVICE_TYPE",
		"ROBORANCH_TARGET_ID",
		"ROBORANCH_IOS_UDID",
		"ROBORANCH_XCODE_DESTINATION",
	),
		"ROBORANCH_DEVICE_ID="+result.ID,
		"ROBORANCH_LEASE_ID="+result.Lease,
		"ROBORANCH_PLATFORM="+string(result.Platform),
		"ROBORANCH_DEVICE_TYPE="+string(result.Type),
		"ROBORANCH_TARGET_ID="+result.Serial,
	)
	cmd.Env = append(cmd.Env, rt.backends.forDevice(device).environment(device)...)
	childErr := cmd.Run()
	releaseErr := a.releaseDevice(context.Background(), rt, device, result.Lease)
	if childErr != nil {
		if releaseErr != nil {
			fmt.Fprintf(a.stderr, "roboranch: release failed; lease retained: %s\n", releaseErr)
		}
		var exitErr *exec.ExitError
		if errors.As(childErr, &exitErr) {
			return commandError{code: exitErr.ExitCode(), err: fmt.Errorf("command exited with %d", exitErr.ExitCode())}
		}
		return commandError{code: exitUnavailable, err: childErr}
	}
	if releaseErr != nil {
		return releaseErr
	}
	return nil
}

func (a *App) cmdRepair(args []string) error {
	fs := newFlagSet("repair", a.stderr)
	id := fs.String("id", "", "device id")
	all := fs.Bool("all", false, "repair all virtual devices")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	if *id == "" && !*all {
		return commandError{code: exitUsage, err: fmt.Errorf("repair requires --id or --all")}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	if *id != "" {
		device, ok := findDevice(rt.config.Devices, *id)
		if !ok {
			return commandError{code: exitUsage, err: fmt.Errorf("unknown device id %q", *id)}
		}
		if !rt.backends.forDevice(device).repairable(device) {
			return commandError{code: exitUsage, err: fmt.Errorf("%s is not repairable", device.ID)}
		}
	}
	healthy, repaired, failed, skipped := 0, 0, 0, 0
	for _, device := range rt.config.Devices {
		backend := rt.backends.forDevice(device)
		if !backend.repairable(device) {
			continue
		}
		if *id != "" && device.ID != *id {
			continue
		}
		if rt.store.locked(device.ID) {
			if stale, _ := rt.store.stale(device.ID, time.Now()); !stale {
				skipped++
				fmt.Fprintf(a.stderr, "roboranch: repair: %s skipped; active lease\n", device.ID)
				continue
			}
		}
		if backend.health(context.Background(), device).healthy {
			healthy++
			fmt.Fprintf(a.stderr, "roboranch: repair: %s healthy\n", device.ID)
			continue
		}
		fmt.Fprintf(a.stderr, "roboranch: repair: restarting %s\n", device.ID)
		if err := backend.repair(context.Background(), rt.config, device); err != nil {
			failed++
			fmt.Fprintf(a.stderr, "roboranch: repair: %s failed: %s\n", device.ID, err)
			continue
		}
		repaired++
	}
	fmt.Fprintf(a.stderr, "roboranch: repair done (healthy=%d repaired=%d failed=%d skipped=%d)\n", healthy, repaired, failed, skipped)
	if failed > 0 || (*id != "" && skipped > 0) {
		return commandError{code: exitUnhealthy, err: fmt.Errorf("repair failed or was refused for %d target(s)", failed+skipped)}
	}
	return nil
}

func (a *App) cmdGC(args []string) error {
	fs := newFlagSet("gc", a.stderr)
	verbose := fs.Bool("verbose", false, "print reaped leases")
	verboseShort := fs.Bool("v", false, "print reaped leases")
	if err := fs.Parse(args); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	rt, err := a.load()
	if err != nil {
		return err
	}
	var logger func(string, ...any)
	if *verbose || *verboseShort {
		logger = func(format string, values ...any) {
			fmt.Fprintf(a.stderr, "roboranch: "+format+"\n", values...)
		}
	}
	reaped, retained := a.gc(context.Background(), rt, logger)
	if logger != nil {
		fmt.Fprintf(a.stderr, "roboranch: gc done: reaped=%d retained=%d\n", reaped, retained)
	}
	if retained > 0 {
		return commandError{code: exitUnhealthy, err: fmt.Errorf("cleanup failed for %d stale lease(s)", retained)}
	}
	return nil
}

func (a *App) releaseDevice(ctx context.Context, rt *runtimeState, device DeviceConfig, expectedLease string) error {
	if err := rt.store.validateRelease(device.ID, expectedLease); err != nil {
		return commandError{code: exitUsage, err: err}
	}
	claimed, err := rt.store.claimCleanup(device.ID, os.Getpid())
	if err != nil {
		return commandError{code: exitUnavailable, err: err}
	}
	if !claimed {
		return commandError{code: exitUnavailable, err: fmt.Errorf("cleanup already in progress for %s", device.ID)}
	}
	defer rt.store.clearCleanup(device.ID)
	if err := rt.backends.forDevice(device).cleanup(ctx, rt.config, device, a.stderr); err != nil {
		return commandError{code: exitUnhealthy, err: fmt.Errorf("cleanup failed; lease retained for %s: %w", device.ID, err)}
	}
	if err := rt.store.forceRelease(device.ID); err != nil {
		return commandError{code: exitUnavailable, err: err}
	}
	return nil
}

func (a *App) gc(ctx context.Context, rt *runtimeState, verbose func(string, ...any)) (int, int) {
	reaped, retained := 0, 0
	for _, stale := range rt.store.staleLeases(time.Now()) {
		claimed, err := rt.store.claimCleanup(stale.ID, os.Getpid())
		if err != nil {
			retained++
			if verbose != nil {
				verbose("retained %s (%s; cleanup claim failed: %s)", stale.ID, stale.Reason, err)
			}
			continue
		}
		if !claimed {
			if verbose != nil {
				verbose("skipped %s (%s; cleanup already in progress)", stale.ID, stale.Reason)
			}
			continue
		}
		device, configured := findDevice(rt.config.Devices, stale.ID)
		if configured && device.cleanupEnabled() {
			if err := rt.backends.forDevice(device).cleanup(ctx, rt.config, device, a.stderr); err != nil {
				rt.store.clearCleanup(stale.ID)
				retained++
				if verbose != nil {
					verbose("retained %s (%s; cleanup failed: %s)", stale.ID, stale.Reason, err)
				}
				continue
			}
		}
		if err := rt.store.forceRelease(stale.ID); err != nil {
			rt.store.clearCleanup(stale.ID)
			retained++
			if verbose != nil {
				verbose("retained %s (%s; unlock failed: %s)", stale.ID, stale.Reason, err)
			}
			continue
		}
		rt.store.clearCleanup(stale.ID)
		reaped++
		if verbose != nil {
			verbose("reaped %s (%s)", stale.ID, stale.Reason)
		}
	}
	return reaped, retained
}

func (a *App) checkout(ctx context.Context, rt *runtimeState, options checkoutOptions) (CheckoutResult, int, error) {
	deadline := time.Now().Add(options.wait)
	for {
		_, _ = a.gc(ctx, rt, nil)
		result, code, err := a.tryCheckout(ctx, rt, options)
		if err == nil {
			return result, exitOK, nil
		}
		if options.wait <= 0 || time.Now().After(deadline) {
			return CheckoutResult{}, code, err
		}
		select {
		case <-ctx.Done():
			return CheckoutResult{}, exitUnavailable, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *App) tryCheckout(ctx context.Context, rt *runtimeState, options checkoutOptions) (CheckoutResult, int, error) {
	candidates := filterDevices(rt.config.Devices, options.platform, options.deviceType, options.serial, options.labels)
	if len(candidates) == 0 {
		return CheckoutResult{}, exitUsage, fmt.Errorf("no device matches selectors")
	}
	rt.store.orderByLastUsed(candidates)
	hadHealthy := false
	hadLocked := false
	for _, device := range candidates {
		backend := rt.backends.forDevice(device)
		if rt.store.locked(device.ID) {
			hadLocked = true
			fmt.Fprintf(a.stderr, "roboranch: skip: %s already locked\n", device.ID)
			continue
		}
		health := backend.health(ctx, device)
		if !health.healthy {
			if backend.repairable(device) {
				fmt.Fprintf(a.stderr, "roboranch: repair: %s unhealthy; attempting restart\n", device.ID)
				if err := backend.repair(ctx, rt.config, device); err != nil {
					fmt.Fprintf(a.stderr, "roboranch: skip: %s unhealthy: %s\n", device.ID, err)
					continue
				}
			} else {
				fmt.Fprintf(a.stderr, "roboranch: skip: %s unhealthy: %s\n", device.ID, health.reason)
				continue
			}
		}
		hadHealthy = true
		lease, ok, err := rt.store.acquire(device, options.holderPID, options.ttl)
		if err != nil {
			return CheckoutResult{}, exitUnavailable, err
		}
		if !ok {
			hadLocked = true
			fmt.Fprintf(a.stderr, "roboranch: skip: %s already locked\n", device.ID)
			continue
		}
		return CheckoutResult{
			Lease:     lease.LeaseID,
			ID:        lease.ID,
			Platform:  device.Platform,
			Serial:    lease.Serial,
			Type:      lease.Type,
			ExpiresAt: lease.ExpiresAt,
		}, exitOK, nil
	}
	if !hadHealthy {
		return CheckoutResult{}, exitUnhealthy, fmt.Errorf("no healthy device available")
	}
	if hadLocked {
		return CheckoutResult{}, exitUnavailable, fmt.Errorf("no device available; all matching healthy devices are locked")
	}
	return CheckoutResult{}, exitUnavailable, fmt.Errorf("no device available")
}

func (a *App) statuses(ctx context.Context, rt *runtimeState) []DeviceStatus {
	statuses := make([]DeviceStatus, 0, len(rt.config.Devices))
	for _, device := range rt.config.Devices {
		statuses = append(statuses, a.status(ctx, rt, device))
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		return statuses[i].ID < statuses[j].ID
	})
	return statuses
}

func (a *App) status(ctx context.Context, rt *runtimeState, device DeviceConfig) DeviceStatus {
	locked := rt.store.locked(device.ID)
	var lease *Lease
	var holderAlive *bool
	if locked {
		if current, err := rt.store.readLease(device.ID); err == nil {
			if current.Platform == "" {
				current.Platform = device.Platform
			}
			lease = current
			alive := processAlive(current.HolderPID)
			holderAlive = &alive
		}
	}
	health := rt.backends.forDevice(device).health(ctx, device)
	return DeviceStatus{
		ID:           device.ID,
		Platform:     device.Platform,
		Type:         device.Type,
		Serial:       device.Serial,
		Labels:       append([]string(nil), device.Labels...),
		Locked:       locked,
		Healthy:      health.healthy,
		HealthReason: health.reason,
		Lease:        lease,
		HolderAlive:  holderAlive,
	}
}

func parseCheckoutOptions(args []string, defaultTTL time.Duration, withLease bool, stderr io.Writer) (checkoutOptions, error) {
	fs := newFlagSet("checkout", stderr)
	platformRaw := fs.String("platform", string(PlatformAndroid), "target platform")
	typeRaw := fs.String("type", string(DeviceTypeAny), "device type")
	serial := fs.String("serial", "", "serial")
	ttlRaw := fs.String("ttl", defaultTTL.String(), "lease ttl")
	waitRaw := fs.String("wait", defaultWait.String(), "wait timeout")
	holderPID := fs.Int("holder-pid", os.Getpid(), "holder pid")
	jsonMode := fs.Bool("json", false, "print JSON")
	labels := repeatedFlag{}
	fs.Var(&labels, "label", "required label")
	if err := fs.Parse(args); err != nil {
		return checkoutOptions{}, err
	}
	if withLease && *jsonMode {
		return checkoutOptions{}, fmt.Errorf("with-lease does not support --json")
	}
	platform := Platform(*platformRaw)
	switch platform {
	case PlatformAny, PlatformAndroid, PlatformIOS:
	default:
		return checkoutOptions{}, fmt.Errorf("--platform must be android, ios, or any")
	}
	deviceType := DeviceType(*typeRaw)
	switch deviceType {
	case DeviceTypeAny, DeviceTypeEmulator, DeviceTypeSimulator, DeviceTypePhysical:
	default:
		return checkoutOptions{}, fmt.Errorf("--type must be emulator, simulator, device, or any")
	}
	ttl, err := parseDuration(*ttlRaw, defaultTTL)
	if err != nil {
		return checkoutOptions{}, fmt.Errorf("--ttl: %w", err)
	}
	if ttl <= 0 {
		return checkoutOptions{}, fmt.Errorf("--ttl must be greater than zero")
	}
	wait, err := parseDuration(*waitRaw, defaultWait)
	if err != nil {
		return checkoutOptions{}, fmt.Errorf("--wait: %w", err)
	}
	if wait < 0 {
		return checkoutOptions{}, fmt.Errorf("--wait must be zero or greater")
	}
	return checkoutOptions{
		platform:   platform,
		deviceType: deviceType,
		serial:     *serial,
		labels:     labels,
		ttl:        ttl,
		wait:       wait,
		holderPID:  *holderPID,
		json:       *jsonMode,
	}, nil
}

func filterDevices(devices []DeviceConfig, platform Platform, deviceType DeviceType, serial string, labels []string) []DeviceConfig {
	var filtered []DeviceConfig
	for _, device := range devices {
		if platform != "" && platform != PlatformAny && device.Platform != platform {
			continue
		}
		if deviceType != "" && deviceType != DeviceTypeAny && device.Type != deviceType {
			continue
		}
		if serial != "" && device.Serial != serial {
			continue
		}
		matches := true
		for _, label := range labels {
			if !hasLabel(device, label) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, device)
		}
	}
	return filtered
}

func hasLabel(device DeviceConfig, label string) bool {
	for _, candidate := range device.Labels {
		if candidate == label {
			return true
		}
	}
	return false
}

func findDevice(devices []DeviceConfig, id string) (DeviceConfig, bool) {
	for _, device := range devices {
		if device.ID == id {
			return device, true
		}
	}
	return DeviceConfig{}, false
}

func splitCommand(args []string) ([]string, []string, bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return nil, nil, false
}

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func environmentWithout(environment []string, keys ...string) []string {
	excluded := make(map[string]bool, len(keys))
	for _, key := range keys {
		excluded[key] = true
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if !excluded[key] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func exampleConfig() string {
	return `{
  "version": 1,
  "stateDir": "~/.local/share/roboranch",
  "androidSdk": "",
  "defaultTTL": "30m",
  "repairTimeout": "2m",
  "devices": [
    {
      "id": "ranch-hand-1",
      "type": "emulator",
      "serial": "emulator-5554",
      "launchdLabel": "com.roboranch.pool-1",
      "systemdUnit": "roboranch-pool-1.service",
      "labels": ["emulator", "api36", "x86_64", "pool-1"],
      "notes": "Example AVD loaded from a ci-clean snapshot"
    },
    {
      "id": "physical-1",
      "type": "device",
      "serial": "REPLACE_WITH_USB_SERIAL",
      "labels": ["device", "physical"],
      "cleanup": {"enabled": false},
      "notes": "Physical devices are not cleaned unless cleanup.enabled is true"
    }
  ]
}
`
}
