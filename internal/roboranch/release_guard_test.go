package roboranch

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// `release --id X` with no --lease force-releases whoever holds the device. It exists
// for repair: a lease whose holder died leaves the device locked, and something has to
// break that.
//
// But the pool is shared — CI jobs and agent sessions queue on the same devices through
// the same checkout, by design. So the repair command is also the command that yanks a
// simulator out from under a running test, and it looks identical to the unblock you
// reach for when `checkout` says the pool is full. Used that way against a live CI job
// it kills the job, and the failure surfaces as a flaky UI test rather than as anything
// pointing back here.
//
// A holder that is alive on this host is not a lease that needs repairing. Refuse, and
// make --force the thing you have to type when you mean it.
func writeHeldLease(t *testing.T, store *LeaseStore, device DeviceConfig, holderPID int) Lease {
	t.Helper()
	lease := Lease{
		LeaseID:    "held",
		ID:         device.ID,
		Serial:     device.Serial,
		Type:       device.Type,
		HolderPID:  holderPID,
		Hostname:   store.hostname,
		AcquiredAt: time.Now().UTC(),
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}
	if err := os.WriteFile(store.lockPath(device.ID), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(store.leasePath(device.ID), lease, 0o644); err != nil {
		t.Fatal(err)
	}
	return lease
}

// A process that is alive for the duration of the test and nothing else.
func startLiveHolder(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func releaseTestApp() *App {
	return &App{
		runner: runnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
			return "", nil
		}),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
}

func TestReleaseRefusesADeviceWhoseHolderIsStillAlive(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	lease := writeHeldLease(t, store, device, startLiveHolder(t))
	app := releaseTestApp()
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, app.runner),
	}

	err = app.releaseDevice(context.Background(), rt, device, "")

	if err == nil {
		t.Fatal("expected release to refuse a device whose holder is alive")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("the refusal must name the way through: %v", err)
	}
	current, readErr := store.readLease(device.ID)
	if readErr != nil || current.LeaseID != lease.LeaseID {
		t.Fatalf("the lease must survive the refusal: lease=%#v err=%v", current, readErr)
	}
}

func TestReleaseWithTheHoldersOwnLeaseIdIsAllowed(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	lease := writeHeldLease(t, store, device, startLiveHolder(t))
	app := releaseTestApp()
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, app.runner),
	}

	// Naming the lease is how the holder releases its own device; the guard is only
	// for the unqualified form.
	if err := app.releaseDevice(context.Background(), rt, device, lease.LeaseID); err != nil {
		t.Fatalf("a holder releasing its own lease must succeed: %v", err)
	}
	if store.locked(device.ID) {
		t.Fatal("the device must be free afterwards")
	}
}

func TestReleaseRepairsALeaseWhoseHolderIsGone(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	// A pid that has already exited: the case the unqualified form exists for.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	writeHeldLease(t, store, device, dead.Process.Pid)
	app := releaseTestApp()
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, app.runner),
	}

	if err := app.releaseDevice(context.Background(), rt, device, ""); err != nil {
		t.Fatalf("a dead holder is exactly what release repairs: %v", err)
	}
	if store.locked(device.ID) {
		t.Fatal("the device must be free afterwards")
	}
}

func TestForceReleasesADeviceWhoseHolderIsAlive(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	writeHeldLease(t, store, device, startLiveHolder(t))
	app := releaseTestApp()
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, app.runner),
	}

	if err := app.forceReleaseDevice(context.Background(), rt, device); err != nil {
		t.Fatalf("--force must still get through: %v", err)
	}
	if store.locked(device.ID) {
		t.Fatal("the device must be free afterwards")
	}
}
