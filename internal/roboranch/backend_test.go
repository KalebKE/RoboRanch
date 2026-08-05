package roboranch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type runnerFunc func(context.Context, string, ...string) (string, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}

func TestIOSSimulatorHealth(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		available   bool
		udid        string
		healthy     bool
		reasonMatch string
	}{
		{name: "booted", state: "Booted", available: true, udid: "SIM-1", healthy: true},
		{name: "shutdown", state: "Shutdown", available: true, udid: "SIM-1", reasonMatch: "shutdown"},
		{name: "unavailable", state: "Shutdown", available: false, udid: "SIM-1", reasonMatch: "runtime is unavailable"},
		{name: "missing", state: "Booted", available: true, udid: "OTHER", reasonMatch: "not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := simctlDeviceList{Devices: map[string][]simctlDevice{
				"com.apple.CoreSimulator.SimRuntime.iOS": {
					{UDID: test.udid, State: test.state, IsAvailable: test.available},
				},
			}}
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
				if name != "xcrun" || strings.Join(args, " ") != "simctl list devices --json" {
					t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
				}
				return string(data), nil
			})}
			health := backend.health(context.Background(), DeviceConfig{
				ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
			})
			if health.healthy != test.healthy {
				t.Fatalf("expected healthy=%v, got %#v", test.healthy, health)
			}
			if test.reasonMatch != "" && !strings.Contains(health.reason, test.reasonMatch) {
				t.Fatalf("expected reason containing %q, got %q", test.reasonMatch, health.reason)
			}
		})
	}
}

func TestIOSSimulatorCleanupErasesAndWarmBoots(t *testing.T) {
	var calls []string
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	})}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	if err := backend.cleanup(context.Background(), Config{}, device, ioDiscard{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"xcrun simctl shutdown SIM-1",
		"xcrun simctl erase SIM-1",
		"xcrun simctl bootstatus SIM-1 -b",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected cleanup calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestIOSSimulatorCleanupStopsAfterEraseFailure(t *testing.T) {
	var calls []string
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "simctl erase") {
			return "", errors.New("erase failed")
		}
		return "", nil
	})}
	err := backend.cleanup(context.Background(), Config{}, DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
	}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "erase simulator") {
		t.Fatalf("expected erase failure, got %v", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "bootstatus") {
		t.Fatalf("cleanup must not warm-boot after a failed erase: %v", calls)
	}
}

func TestPlatformSpecificEnvironment(t *testing.T) {
	android := androidBackend{}.environment(DeviceConfig{Serial: "emulator-5554"})
	if strings.Join(android, "\n") != "ANDROID_SERIAL=emulator-5554" {
		t.Fatalf("unexpected Android environment: %v", android)
	}
	ios := iosBackend{}.environment(DeviceConfig{Type: DeviceTypeSimulator, Serial: "SIM-1"})
	joined := strings.Join(ios, "\n")
	if !strings.Contains(joined, "ROBORANCH_IOS_UDID=SIM-1") ||
		!strings.Contains(joined, "ROBORANCH_XCODE_DESTINATION=platform=iOS Simulator,id=SIM-1") {
		t.Fatalf("unexpected iOS simulator environment: %v", ios)
	}
	iphone := iosBackend{}.environment(DeviceConfig{Type: DeviceTypePhysical, Serial: "PHONE-1"})
	if !strings.Contains(strings.Join(iphone, "\n"), "platform=iOS,id=PHONE-1") {
		t.Fatalf("unexpected physical iPhone environment: %v", iphone)
	}
}

func TestIOSPhysicalHealthRequiresTestReadyDevice(t *testing.T) {
	tests := []struct {
		name          string
		pairing       string
		developerMode string
		ddi           bool
		locked        bool
		lockResponse  bool
		healthy       bool
		reasonMatch   string
	}{
		{name: "ready", pairing: "paired", developerMode: "enabled", ddi: true, lockResponse: true, healthy: true},
		{name: "unpaired", pairing: "unpaired", developerMode: "enabled", ddi: true, reasonMatch: "not paired"},
		{name: "developer mode disabled", pairing: "paired", developerMode: "disabled", ddi: true, reasonMatch: "Developer Mode"},
		{name: "offline", pairing: "paired", developerMode: "enabled", reasonMatch: "developer services"},
		{name: "locked", pairing: "paired", developerMode: "enabled", ddi: true, locked: true, lockResponse: true, reasonMatch: "locked"},
		{name: "unknown lock response", pairing: "paired", developerMode: "enabled", ddi: true, reasonMatch: "did not report"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := deviceCtlFixtureRunner(t, test.pairing, test.developerMode, test.ddi, test.locked, test.lockResponse)
			backend := iosBackend{runner: runner}
			health := backend.health(context.Background(), DeviceConfig{
				ID: "iphone", Platform: PlatformIOS, Type: DeviceTypePhysical, Serial: "PHONE-UDID",
			})
			if health.healthy != test.healthy {
				t.Fatalf("expected healthy=%v, got %#v", test.healthy, health)
			}
			if test.reasonMatch != "" && !strings.Contains(health.reason, test.reasonMatch) {
				t.Fatalf("expected reason containing %q, got %q", test.reasonMatch, health.reason)
			}
		})
	}
}

func deviceCtlFixtureRunner(t *testing.T, pairing, developerMode string, ddi, locked, includeLock bool) CommandRunner {
	t.Helper()
	return runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		if name != "xcrun" || len(args) == 0 || args[0] != "devicectl" {
			t.Fatalf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		var output string
		for i, arg := range args {
			if arg == "--json-output" && i+1 < len(args) {
				output = args[i+1]
				break
			}
		}
		if output == "" {
			t.Fatal("devicectl command did not include --json-output")
		}
		var result any
		call := strings.Join(args, " ")
		if strings.Contains(call, "list devices") || strings.Contains(call, "device info details") {
			device := deviceCtlDevice{Identifier: "CORE-DEVICE-ID"}
			device.ConnectionProperties.PairingState = pairing
			device.DeviceProperties.DeveloperModeStatus = developerMode
			device.HardwareProperties.UDID = "PHONE-UDID"
			if strings.Contains(call, "list devices") {
				// CoreDevice's list can be stale until a direct details query
				// establishes the developer tunnel.
				device.DeviceProperties.DDIServicesAvailable = false
				result = deviceCtlListResult{Devices: []deviceCtlDevice{device}}
			} else {
				device.DeviceProperties.DDIServicesAvailable = ddi
				result = device
			}
		} else if strings.Contains(call, "device info lockState") {
			if includeLock {
				result = map[string]any{"device": map[string]any{"isLocked": locked}}
			} else {
				result = map[string]any{"device": map[string]any{"name": "Test iPhone"}}
			}
		} else {
			t.Fatalf("unexpected devicectl operation: %s", call)
		}
		envelope := map[string]any{
			"info":   map[string]any{"outcome": "success"},
			"result": result,
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return "", nil
	})
}

func TestAndroidHealthRejectsAWedgedDevice(t *testing.T) {
	// `adb get-state` answers "device" long after the UI is unusable: a system_server ANR
	// leaves a dialog owning the screen and stalls broadcast delivery, but adb keeps
	// replying. The pool therefore handed out wedged emulators and `repair` blessed them
	// ("healthy repaired=0") while the dialog was still up. Observed with three of four
	// emulators wedged simultaneously and every one reported healthy.
	tests := []struct {
		name    string
		focus   string
		healthy bool
	}{
		{
			name:    "idle launcher is healthy",
			focus:   "  mCurrentFocus=Window{2c55bb1 u0 com.google.android.apps.nexuslauncher/.NexusLauncherActivity}",
			healthy: true,
		},
		{
			name:  "system ANR is not",
			focus: "  mCurrentFocus=Window{85b3cb9 u0 Application Not Responding: system}",
		},
		{
			name:  "an app ANR is not either",
			focus: "  mCurrentFocus=Window{2229c74 u0 Application Not Responding: com.google.android.apps.wellbeing}",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := runnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
				for _, a := range args {
					if a == "get-state" {
						return "device\n", nil
					}
					if a == "window" {
						return test.focus + "\n", nil
					}
				}
				return "", nil
			})
			backend := androidBackend{adb: ADB{path: "adb", runner: runner}}
			got := backend.health(context.Background(), DeviceConfig{Serial: "emulator-5554"})
			if got.healthy != test.healthy {
				t.Fatalf("healthy = %v, want %v (reason %q)", got.healthy, test.healthy, got.reason)
			}
			if !test.healthy && !strings.Contains(strings.ToLower(got.reason), "not responding") {
				t.Fatalf("reason should name the ANR, got %q", got.reason)
			}
		})
	}
}
