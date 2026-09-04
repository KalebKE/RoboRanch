package roboranch

import (
	"strings"
	"testing"
)

func TestConfigDefaultsLegacyDevicesToAndroid(t *testing.T) {
	cfg := Config{
		Version: 1,
		Devices: []DeviceConfig{
			{ID: "legacy-emulator", Type: DeviceTypeEmulator, Serial: "emulator-5554"},
			{ID: "legacy-device", Type: DeviceTypePhysical, Serial: "USB123"},
		},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	for _, device := range cfg.Devices {
		if device.Platform != PlatformAndroid {
			t.Fatalf("expected legacy device %s to default to Android, got %q", device.ID, device.Platform)
		}
	}
}

func TestConfigAcceptsIOSSimulatorAndPhysicalDevice(t *testing.T) {
	disabled := false
	cfg := Config{
		Version: 1,
		Devices: []DeviceConfig{
			{ID: "sim-1", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-UDID"},
			{
				ID:       "iphone-1",
				Platform: PlatformIOS,
				Type:     DeviceTypePhysical,
				Serial:   "PHONE-UDID",
				Cleanup:  &CleanupConfig{Enabled: &disabled},
			},
		},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Devices[0].cleanupEnabled() {
		t.Fatal("expected iOS simulator cleanup to default to enabled")
	}
	if cfg.Devices[1].cleanupEnabled() {
		t.Fatal("expected physical iPhone cleanup to be disabled")
	}
}

func TestConfigAcceptsIOSBootLimitWithShutdownCleanup(t *testing.T) {
	maxBooted := 1
	cfg := Config{
		Version: 1,
		Limits:  LimitsConfig{IOSSimulators: IOSSimulatorLimits{MaxBooted: &maxBooted}},
		Devices: []DeviceConfig{{
			ID:       "sim-1",
			Platform: PlatformIOS,
			Type:     DeviceTypeSimulator,
			Serial:   "SIM-UDID",
			Cleanup:  &CleanupConfig{Mode: string(CleanupShutdown)},
		}},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	if cfg.iosSimulatorMaxBooted() != 1 {
		t.Fatalf("expected one booted simulator, got %d", cfg.iosSimulatorMaxBooted())
	}
	if cfg.Devices[0].cleanupMode() != CleanupShutdown {
		t.Fatalf("expected shutdown cleanup, got %q", cfg.Devices[0].cleanupMode())
	}
}

func TestConfigRejectsUnsafeIOSBootLimits(t *testing.T) {
	zero := 0
	for _, test := range []struct {
		name  string
		limit *int
		mode  string
		match string
	}{
		{name: "zero", limit: &zero, mode: string(CleanupShutdown), match: "maxBooted must be greater than zero"},
		{name: "cleanup disabled", limit: intPointer(1), mode: string(CleanupNone), match: "must use cleanup mode shutdown"},
		{name: "reset cleanup", limit: intPointer(1), mode: string(CleanupReset), match: "must use cleanup mode shutdown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Version: 1,
				Limits:  LimitsConfig{IOSSimulators: IOSSimulatorLimits{MaxBooted: test.limit}},
				Devices: []DeviceConfig{{
					ID:       "sim-1",
					Platform: PlatformIOS,
					Type:     DeviceTypeSimulator,
					Serial:   "SIM-UDID",
					Cleanup:  &CleanupConfig{Mode: test.mode},
				}},
			}
			err := cfg.applyDefaultsAndValidate()
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}

func TestConfigRejectsAmbiguousCleanup(t *testing.T) {
	enabled := false
	cfg := Config{
		Version: 1,
		Devices: []DeviceConfig{{
			ID:       "sim-1",
			Platform: PlatformIOS,
			Type:     DeviceTypeSimulator,
			Serial:   "SIM-UDID",
			Cleanup:  &CleanupConfig{Enabled: &enabled, Mode: string(CleanupShutdown)},
		}},
	}
	err := cfg.applyDefaultsAndValidate()
	if err == nil || !strings.Contains(err.Error(), "both enabled and mode") {
		t.Fatalf("expected ambiguous cleanup error, got %v", err)
	}
}

func intPointer(value int) *int {
	return &value
}

func TestConfigRejectsInvalidPlatformTypeCombinations(t *testing.T) {
	enabled := true
	tests := []struct {
		name   string
		device DeviceConfig
		match  string
	}{
		{
			name:   "android simulator",
			device: DeviceConfig{ID: "bad", Platform: PlatformAndroid, Type: DeviceTypeSimulator, Serial: "x"},
			match:  "emulator or device",
		},
		{
			name:   "ios emulator",
			device: DeviceConfig{ID: "bad", Platform: PlatformIOS, Type: DeviceTypeEmulator, Serial: "x"},
			match:  "simulator or device",
		},
		{
			name: "physical iphone cleanup",
			device: DeviceConfig{
				ID:       "bad",
				Platform: PlatformIOS,
				Type:     DeviceTypePhysical,
				Serial:   "x",
				Cleanup:  &CleanupConfig{Enabled: &enabled},
			},
			match: "cannot enable cleanup",
		},
		{
			name: "ios android fields",
			device: DeviceConfig{
				ID:            "bad",
				Platform:      PlatformIOS,
				Type:          DeviceTypeSimulator,
				Serial:        "x",
				LaunchdLabel:  "com.example.android",
				EmulatorFlags: []string{"-no-window"},
			},
			match: "Android emulator fields",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Version: 1, Devices: []DeviceConfig{test.device}}
			err := cfg.applyDefaultsAndValidate()
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("expected error containing %q, got %v", test.match, err)
			}
		})
	}
}
