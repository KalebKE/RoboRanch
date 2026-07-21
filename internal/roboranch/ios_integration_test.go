package roboranch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIOSSimulatorLeaseCleanupIntegration(t *testing.T) {
	if os.Getenv("ROBORANCH_IOS_INTEGRATION") != "1" {
		t.Skip("set ROBORANCH_IOS_INTEGRATION=1 to run the disposable-simulator test")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("iOS simulator integration requires macOS")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	runner := execCommandRunner{}

	runtimeOut, err := runner.Run(ctx, "xcrun", "simctl", "list", "runtimes", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var runtimes struct {
		Runtimes []struct {
			Identifier           string `json:"identifier"`
			IsAvailable          bool   `json:"isAvailable"`
			SupportedDeviceTypes []struct {
				Identifier    string `json:"identifier"`
				ProductFamily string `json:"productFamily"`
			} `json:"supportedDeviceTypes"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal([]byte(runtimeOut), &runtimes); err != nil {
		t.Fatal(err)
	}
	runtimeID, deviceTypeID := "", ""
	for _, candidate := range runtimes.Runtimes {
		if !candidate.IsAvailable || !strings.Contains(candidate.Identifier, "SimRuntime.iOS") {
			continue
		}
		for _, deviceType := range candidate.SupportedDeviceTypes {
			if deviceType.ProductFamily == "iPhone" {
				runtimeID = candidate.Identifier
				deviceTypeID = deviceType.Identifier
				break
			}
		}
		if deviceTypeID != "" {
			break
		}
	}
	if runtimeID == "" || deviceTypeID == "" {
		t.Fatal("no available iOS runtime with an iPhone device type")
	}

	name := fmt.Sprintf("RoboRanch-Integration-%d", os.Getpid())
	createOut, err := runner.Run(ctx, "xcrun", "simctl", "create", name, deviceTypeID, runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	udid := strings.TrimSpace(createOut)
	if udid == "" {
		t.Fatal("simctl create returned an empty UDID")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = runner.Run(cleanupCtx, "xcrun", "simctl", "shutdown", udid)
		if _, err := runner.Run(cleanupCtx, "xcrun", "simctl", "delete", udid); err != nil {
			t.Errorf("delete disposable simulator: %v", err)
		}
	})

	device := DeviceConfig{
		ID:       "ios-integration",
		Platform: PlatformIOS,
		Type:     DeviceTypeSimulator,
		Serial:   udid,
		Labels:   []string{"integration"},
	}
	cfg := Config{
		Version:       1,
		StateDir:      t.TempDir(),
		DefaultTTL:    "5m",
		RepairTimeout: "2m",
		Devices:       []DeviceConfig{device},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	store, err := newLeaseStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := &App{runner: runner, stdout: &stdout, stderr: &stderr}
	rt := &runtimeState{
		config:   cfg,
		store:    store,
		backends: newBackendRegistry(cfg, runner),
	}
	result, code, err := app.checkout(ctx, rt, checkoutOptions{
		platform:   PlatformIOS,
		deviceType: DeviceTypeSimulator,
		ttl:        5 * time.Minute,
		holderPID:  os.Getpid(),
	})
	if err != nil || code != exitOK {
		t.Fatalf("checkout failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	if result.Serial != udid || result.Platform != PlatformIOS {
		t.Fatalf("unexpected checkout result: %#v", result)
	}

	if _, err := runner.Run(ctx, "xcrun", "simctl", "spawn", udid, "defaults", "write", "com.roboranch.integration", "marker", "-bool", "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.releaseDevice(ctx, rt, device, result.Lease); err != nil {
		t.Fatalf("release failed: %v stderr=%s", err, stderr.String())
	}
	if store.locked(device.ID) {
		t.Fatal("successful cleanup must release the lease")
	}
	if health := rt.backends.forDevice(device).health(ctx, device); !health.healthy {
		t.Fatalf("simulator was not returned warm: %#v", health)
	}
	if _, err := runner.Run(ctx, "xcrun", "simctl", "spawn", udid, "defaults", "read", "com.roboranch.integration", "marker"); err == nil {
		t.Fatal("simulator marker survived erase")
	}
}

func TestIOSPhysicalLeaseIntegration(t *testing.T) {
	udid := os.Getenv("ROBORANCH_IPHONE_INTEGRATION_UDID")
	if udid == "" {
		t.Skip("set ROBORANCH_IPHONE_INTEGRATION_UDID to run the physical-iPhone lease test")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("physical iPhone integration requires macOS")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	device := DeviceConfig{
		ID:       "iphone-integration",
		Platform: PlatformIOS,
		Type:     DeviceTypePhysical,
		Serial:   udid,
		Labels:   []string{"integration", "physical"},
	}
	cfg := Config{
		Version:       1,
		StateDir:      t.TempDir(),
		DefaultTTL:    "5m",
		RepairTimeout: "2m",
		Devices:       []DeviceConfig{device},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	runner := execCommandRunner{}
	store, err := newLeaseStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := &App{runner: runner, stdout: &stdout, stderr: &stderr}
	rt := &runtimeState{
		config:   cfg,
		store:    store,
		backends: newBackendRegistry(cfg, runner),
	}

	health := rt.backends.forDevice(device).health(ctx, device)
	if !health.healthy {
		t.Fatalf("physical iPhone is not test-ready: %s", health.reason)
	}
	result, code, err := app.checkout(ctx, rt, checkoutOptions{
		platform:   PlatformIOS,
		deviceType: DeviceTypePhysical,
		ttl:        5 * time.Minute,
		holderPID:  os.Getpid(),
	})
	if err != nil || code != exitOK {
		t.Fatalf("checkout failed: code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	if result.Serial != udid || result.Platform != PlatformIOS || result.Type != DeviceTypePhysical {
		t.Fatalf("unexpected checkout result: %#v", result)
	}
	environment := strings.Join(rt.backends.forDevice(device).environment(device), "\n")
	if !strings.Contains(environment, "ROBORANCH_IOS_UDID="+udid) ||
		!strings.Contains(environment, "ROBORANCH_XCODE_DESTINATION=platform=iOS,id="+udid) {
		t.Fatalf("unexpected physical-iPhone environment: %s", environment)
	}
	if err := app.releaseDevice(ctx, rt, device, result.Lease); err != nil {
		t.Fatalf("release failed: %v stderr=%s", err, stderr.String())
	}
	if store.locked(device.ID) {
		t.Fatal("physical iPhone lease remained locked after release")
	}
}
