package roboranch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A pooled emulator resumed from a snapshot can come back months behind. Sixty seconds is
// Measured, not guessed: a snapshot-resumed emulator sits 80-130s behind until
// Android's NTP poll corrects it (the poll interval is 18 hours), and the whole
// healthy pool passes every TLS-touching E2E in that state. 60s would therefore
// mark a working pool unhealthy after every launchd restart. What actually broke
// certificate validation was a 4.5-MONTH lag on a stale snapshot. 15 minutes is
// far above resume-lag noise and far below anything a cert's validity window
// notices, so it separates "NTP hasn't caught up yet" from "this device will
// fail every handshake".
const maxClockSkew = 15 * time.Minute

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

type deviceHealth struct {
	healthy bool
	reason  string
}

type deviceBackend interface {
	health(context.Context, DeviceConfig) deviceHealth
	repair(context.Context, Config, DeviceConfig) error
	cleanup(context.Context, Config, DeviceConfig, io.Writer) error
	repairable(DeviceConfig) bool
	environment(DeviceConfig) []string
}

type backendRegistry struct {
	android androidBackend
	ios     iosBackend
}

func newBackendRegistry(cfg Config, runner CommandRunner) backendRegistry {
	return backendRegistry{
		android: androidBackend{
			adb:  newADB(cfg.adbPath(), runner),
			host: newHostManager(runner),
		},
		ios: iosBackend{runner: runner},
	}
}

func (r backendRegistry) forDevice(device DeviceConfig) deviceBackend {
	if device.Platform == PlatformIOS {
		return r.ios
	}
	return r.android
}

type androidBackend struct {
	adb  ADB
	host HostManager
}

func (b androidBackend) health(ctx context.Context, device DeviceConfig) deviceHealth {
	if !b.adb.healthy(ctx, device) {
		return deviceHealth{reason: "ADB target is offline or unavailable"}
	}
	// Reachable is not the same as usable. Reporting a wedged emulator healthy is worse
	// than reporting it down: repair skips it, the pool keeps handing it out, and every
	// consumer fails somewhere unrelated to the reason.
	if b.adb.wedged(ctx, device) {
		return deviceHealth{reason: "an Application Not Responding dialog owns the screen"}
	}
	// Same class of problem as wedged, different symptom: reachable, idle, and useless.
	// A clock this far out fails every TLS chain on the device.
	if skew, ok := b.adb.clockSkew(ctx, device); ok && absDuration(skew) > maxClockSkew {
		return deviceHealth{reason: fmt.Sprintf(
			"clock is %s from this host; TLS chains will fail notBefore validation",
			absDuration(skew).Round(time.Second),
		)}
	}
	return deviceHealth{healthy: true}
}

func (b androidBackend) repair(ctx context.Context, cfg Config, device DeviceConfig) error {
	return b.host.repair(ctx, cfg, b.adb, device)
}

func (b androidBackend) cleanup(ctx context.Context, cfg Config, device DeviceConfig, stderr io.Writer) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, cfg.repairTimeoutDuration())
	defer cancel()
	return b.adb.cleanup(cleanupCtx, device, stderr)
}

func (b androidBackend) repairable(device DeviceConfig) bool {
	return device.Type == DeviceTypeEmulator
}

func (b androidBackend) environment(device DeviceConfig) []string {
	return []string{"ANDROID_SERIAL=" + device.Serial}
}

type iosBackend struct {
	runner CommandRunner
}

type simctlDevice struct {
	Name        string `json:"name"`
	UDID        string `json:"udid"`
	State       string `json:"state"`
	IsAvailable bool   `json:"isAvailable"`
}

type simctlDeviceList struct {
	Devices map[string][]simctlDevice `json:"devices"`
}

func (b iosBackend) health(ctx context.Context, device DeviceConfig) deviceHealth {
	if device.Type == DeviceTypeSimulator {
		return b.simulatorHealth(ctx, device)
	}
	return b.physicalHealth(ctx, device)
}

func (b iosBackend) simulatorHealth(ctx context.Context, device DeviceConfig) deviceHealth {
	listing, err := b.simulatorList(ctx)
	if err != nil {
		return deviceHealth{reason: err.Error()}
	}
	for _, runtimeDevices := range listing.Devices {
		for _, candidate := range runtimeDevices {
			if candidate.UDID != device.Serial {
				continue
			}
			if !candidate.IsAvailable {
				return deviceHealth{reason: "simulator runtime is unavailable"}
			}
			if candidate.State != "Booted" {
				return deviceHealth{reason: "simulator is " + strings.ToLower(candidate.State)}
			}
			return deviceHealth{healthy: true}
		}
	}
	return deviceHealth{reason: "simulator UDID was not found"}
}

func (b iosBackend) simulatorList(ctx context.Context) (simctlDeviceList, error) {
	out, err := b.runner.Run(ctx, "xcrun", "simctl", "list", "devices", "--json")
	if err != nil {
		return simctlDeviceList{}, fmt.Errorf("simctl list failed: %w", err)
	}
	var listing simctlDeviceList
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		return simctlDeviceList{}, fmt.Errorf("invalid simctl JSON: %w", err)
	}
	return listing, nil
}

func (b iosBackend) bootedSimulators(ctx context.Context) ([]simctlDevice, error) {
	listing, err := b.simulatorList(ctx)
	if err != nil {
		return nil, err
	}
	var booted []simctlDevice
	for runtimeID, devices := range listing.Devices {
		if !strings.Contains(runtimeID, ".iOS-") {
			continue
		}
		for _, device := range devices {
			if device.IsAvailable && device.State == "Booted" {
				booted = append(booted, device)
			}
		}
	}
	return booted, nil
}

func (b iosBackend) repair(ctx context.Context, cfg Config, device DeviceConfig) error {
	if device.Type != DeviceTypeSimulator {
		return fmt.Errorf("%s is not a simulator", device.ID)
	}
	bootCtx, cancel := context.WithTimeout(ctx, deviceBootTimeout(cfg, device))
	defer cancel()
	if _, err := b.runner.Run(bootCtx, "xcrun", "simctl", "bootstatus", device.Serial, "-b"); err != nil {
		return fmt.Errorf("boot simulator %s: %w", device.ID, err)
	}
	return nil
}

func (b iosBackend) cleanup(ctx context.Context, cfg Config, device DeviceConfig, stderr io.Writer) error {
	mode := device.cleanupMode()
	if mode == CleanupNone {
		return nil
	}
	if device.Type != DeviceTypeSimulator {
		return fmt.Errorf("cleanup is unsupported for physical iOS device %s", device.ID)
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, deviceBootTimeout(cfg, device))
	defer cancel()
	fmt.Fprintf(stderr, "roboranch: cleaning up %s (%s)\n", device.ID, device.Serial)
	if err := b.shutdownSimulator(cleanupCtx, device); err != nil {
		return err
	}
	if mode == CleanupShutdown {
		fmt.Fprintf(stderr, "roboranch: cleanup done for %s (shutdown)\n", device.ID)
		return nil
	}
	if _, err := b.runner.Run(cleanupCtx, "xcrun", "simctl", "erase", device.Serial); err != nil {
		return fmt.Errorf("erase simulator %s: %w", device.ID, err)
	}
	if _, err := b.runner.Run(cleanupCtx, "xcrun", "simctl", "bootstatus", device.Serial, "-b"); err != nil {
		return fmt.Errorf("warm boot simulator %s: %w", device.ID, err)
	}
	fmt.Fprintf(stderr, "roboranch: cleanup done for %s\n", device.ID)
	return nil
}

func (b iosBackend) shutdownSimulator(ctx context.Context, device DeviceConfig) error {
	if _, err := b.runner.Run(ctx, "xcrun", "simctl", "shutdown", device.Serial); err == nil {
		return nil
	} else if health := b.simulatorHealth(ctx, device); health.reason == "simulator is shutdown" {
		return nil
	} else {
		return fmt.Errorf("shutdown simulator %s: %w", device.ID, err)
	}
}

func (b iosBackend) repairable(device DeviceConfig) bool {
	return device.Type == DeviceTypeSimulator
}

func (b iosBackend) environment(device DeviceConfig) []string {
	platform := "iOS"
	if device.Type == DeviceTypeSimulator {
		platform = "iOS Simulator"
	}
	return []string{
		"ROBORANCH_IOS_UDID=" + device.Serial,
		"ROBORANCH_XCODE_DESTINATION=platform=" + platform + ",id=" + device.Serial,
	}
}

type deviceCtlDevice struct {
	Identifier           string `json:"identifier"`
	ConnectionProperties struct {
		PairingState string `json:"pairingState"`
		TunnelState  string `json:"tunnelState"`
	} `json:"connectionProperties"`
	DeviceProperties struct {
		DeveloperModeStatus  string `json:"developerModeStatus"`
		DDIServicesAvailable bool   `json:"ddiServicesAvailable"`
	} `json:"deviceProperties"`
	HardwareProperties struct {
		UDID         string `json:"udid"`
		SerialNumber string `json:"serialNumber"`
	} `json:"hardwareProperties"`
}

type deviceCtlListResult struct {
	Devices []deviceCtlDevice `json:"devices"`
}

type deviceCtlEnvelope[T any] struct {
	Info struct {
		Outcome string `json:"outcome"`
	} `json:"info"`
	Result T `json:"result"`
}

func (b iosBackend) physicalHealth(ctx context.Context, device DeviceConfig) deviceHealth {
	var listing deviceCtlEnvelope[deviceCtlListResult]
	if err := b.runDeviceCtlJSON(ctx, &listing, "list", "devices"); err != nil {
		return deviceHealth{reason: fmt.Sprintf("devicectl list failed: %s", err)}
	}
	var found *deviceCtlDevice
	for i := range listing.Result.Devices {
		candidate := &listing.Result.Devices[i]
		if device.Serial == candidate.Identifier || device.Serial == candidate.HardwareProperties.UDID || device.Serial == candidate.HardwareProperties.SerialNumber {
			found = candidate
			break
		}
	}
	if found == nil {
		return deviceHealth{reason: "physical iPhone was not found"}
	}
	var details deviceCtlEnvelope[deviceCtlDevice]
	if err := b.runDeviceCtlJSON(ctx, &details, "device", "info", "details", "--device", device.Serial, "--timeout", "10"); err != nil {
		return deviceHealth{reason: fmt.Sprintf("device details check failed: %s", err)}
	}
	current := details.Result
	if current.ConnectionProperties.PairingState != "paired" {
		return deviceHealth{reason: "physical iPhone is not paired"}
	}
	if current.DeviceProperties.DeveloperModeStatus != "enabled" {
		return deviceHealth{reason: "Developer Mode is not enabled"}
	}
	if !current.DeviceProperties.DDIServicesAvailable {
		return deviceHealth{reason: "developer services are unavailable"}
	}
	var lockState deviceCtlEnvelope[map[string]any]
	if err := b.runDeviceCtlJSON(ctx, &lockState, "device", "info", "lockState", "--device", device.Serial, "--timeout", "10"); err != nil {
		return deviceHealth{reason: fmt.Sprintf("lock-state check failed: %s", err)}
	}
	locked, ok := findLockState(lockState.Result)
	if !ok {
		return deviceHealth{reason: "lock-state response did not report whether the iPhone is locked"}
	}
	if locked {
		return deviceHealth{reason: "physical iPhone is locked"}
	}
	return deviceHealth{healthy: true}
}

func (b iosBackend) runDeviceCtlJSON(ctx context.Context, target any, args ...string) error {
	dir, err := os.MkdirTemp("", "roboranch-devicectl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "output.json")
	fullArgs := append([]string{"devicectl"}, args...)
	fullArgs = append(fullArgs, "--json-output", output, "--quiet")
	if _, err := b.runner.Run(ctx, "xcrun", fullArgs...); err != nil {
		return err
	}
	data, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	var metadata struct {
		Info struct {
			Outcome string `json:"outcome"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return err
	}
	if metadata.Info.Outcome != "" && metadata.Info.Outcome != "success" {
		return fmt.Errorf("devicectl outcome was %s", metadata.Info.Outcome)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func findLockState(value any) (bool, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, candidate := range typed {
			normalized := strings.ToLower(key)
			if normalized == "islocked" || normalized == "locked" || normalized == "passcoderequired" {
				if result, ok := candidate.(bool); ok {
					return result, true
				}
			}
		}
		for _, candidate := range typed {
			if result, ok := findLockState(candidate); ok {
				return result, true
			}
		}
	case []any:
		for _, candidate := range typed {
			if result, ok := findLockState(candidate); ok {
				return result, true
			}
		}
	}
	return false, false
}
