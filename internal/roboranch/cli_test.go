package roboranch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestCheckoutOptionsDefaultToAndroid(t *testing.T) {
	options, err := parseCheckoutOptions(nil, time.Minute, false, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if options.platform != PlatformAndroid {
		t.Fatalf("expected default platform android, got %q", options.platform)
	}
	if options.deviceType != DeviceTypeAny {
		t.Fatalf("expected default type any, got %q", options.deviceType)
	}
}

func TestStatusEnrichesLegacyLeaseWithConfiguredPlatform(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	lease := Lease{
		LeaseID:    "legacy",
		ID:         device.ID,
		Serial:     device.Serial,
		Type:       device.Type,
		HolderPID:  os.Getpid(),
		Hostname:   store.hostname,
		AcquiredAt: time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Minute),
	}
	if err := os.WriteFile(store.lockPath(device.ID), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(store.leasePath(device.ID), lease, 0o644); err != nil {
		t.Fatal(err)
	}
	listing, err := json.Marshal(simctlDeviceList{Devices: map[string][]simctlDevice{
		"runtime": {{UDID: device.Serial, State: "Booted", IsAvailable: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		return string(listing), nil
	})
	app := &App{runner: runner, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, runner),
	}
	status := app.status(context.Background(), rt, device)
	if status.Lease == nil || status.Lease.Platform != PlatformIOS {
		t.Fatalf("legacy lease platform was not enriched: %#v", status.Lease)
	}
}
