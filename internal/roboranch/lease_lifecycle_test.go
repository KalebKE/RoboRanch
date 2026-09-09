package roboranch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLeaseHolderAndTTLContracts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pid   int
		ttl   time.Duration
		stale bool
	}{
		{"standalone", 0, time.Hour, false},
		{"abandoned standalone", 0, -time.Second, true},
		{"dead supervisor", 2147483632, time.Hour, true},
		{"live supervisor", os.Getpid(), time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := newLeaseStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			device := DeviceConfig{ID: "test", Serial: "synthetic", Type: DeviceTypeEmulator}
			if _, ok, err := store.acquire(device, tc.pid, tc.ttl); err != nil || !ok {
				t.Fatalf("acquire: %v %v", ok, err)
			}
			if got, reason := store.stale(device.ID, time.Now()); got != tc.stale {
				t.Fatalf("stale=%v reason=%s", got, reason)
			}
		})
	}
}

func TestGCRechecksReplacementLease(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "test", Serial: "synthetic", Type: DeviceTypeEmulator}
	first, _, err := store.acquire(device, 0, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stale := store.staleLeases(time.Now())
	if len(stale) != 1 {
		t.Fatal("expected expired lease in initial scan")
	}
	if err := store.release(device.ID, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.acquire(device, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(context.Context, string, ...string) (string, error) {
		t.Error("GC touched the replacement device")
		return "", nil
	})
	app := &App{stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{config: Config{Devices: []DeviceConfig{device}}, store: store, backends: newBackendRegistry(Config{}, runner)}
	if reaped, retained := app.reapStaleDevice(context.Background(), rt, stale[0].ID, nil); reaped != 0 || retained != 0 {
		t.Fatalf("gc=%d,%d", reaped, retained)
	}
	if err := app.releaseDevice(context.Background(), rt, device, first.LeaseID); err == nil {
		t.Fatal("old token released replacement lease")
	}
	current, err := store.readLease(device.ID)
	if err != nil || current.LeaseID != second.LeaseID {
		t.Fatalf("replacement lost: %v", err)
	}
}

func TestCleanupSerializesAcquisitionAndGC(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "test", Serial: "synthetic", Type: DeviceTypeEmulator}
	lease, _, err := store.acquire(device, 0, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started, proceed := make(chan struct{}), make(chan struct{})
	runner := runnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "pm list packages") {
			close(started)
			<-proceed
		}
		return "", nil
	})
	app := &App{stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{config: Config{Devices: []DeviceConfig{device}}, store: store, backends: newBackendRegistry(Config{}, runner)}
	done := make(chan error, 1)
	go func() { done <- app.releaseDevice(context.Background(), rt, device, lease.LeaseID) }()
	<-started
	other, err := newLeaseStore(store.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := other.acquire(device, 0, time.Hour); err != nil || ok {
		t.Errorf("acquired during cleanup: ok=%v err=%v", ok, err)
	}
	if reaped, retained := app.gc(context.Background(), rt, nil); reaped != 0 || retained != 0 {
		t.Errorf("competing gc=%d,%d", reaped, retained)
	}
	close(proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok, err := other.acquire(device, 0, time.Hour); err != nil || !ok {
		t.Fatalf("acquire after cleanup: %v %v", ok, err)
	}
}

func TestAndroidCleanupFailureRetainsLeaseAndEvidence(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "test", Serial: "synthetic", Type: DeviceTypeEmulator}
	lease, _, err := store.acquire(device, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		if strings.Contains(call, "logcat -c") {
			t.Error("cleanup erased crash evidence")
		}
		if strings.Contains(call, "pm list packages") {
			return "package:com.example.test", nil
		}
		if strings.Contains(call, "uninstall") {
			return "Failure [DELETE_FAILED_INTERNAL_ERROR]", nil
		}
		return "", nil
	})
	app := &App{stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{config: Config{Devices: []DeviceConfig{device}}, store: store, backends: newBackendRegistry(Config{}, runner)}
	if err := app.releaseDevice(context.Background(), rt, device, lease.LeaseID); err == nil || !strings.Contains(err.Error(), "lease retained") {
		t.Fatalf("expected retained failure: %v", err)
	}
	if !store.locked(device.ID) {
		t.Fatal("failed uninstall unlocked device")
	}
	data, err := os.ReadFile(filepath.Join(store.stateDir, "logs", "leases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), lease.LeaseID) || !strings.Contains(string(data), "cleanup_failed") {
		t.Fatalf("audit missing failure or exposing token: %s", data)
	}
}

func TestAndroidCleanupHasDeadline(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	backend := androidBackend{adb: newADB("adb", runner)}
	err := backend.cleanup(context.Background(), Config{RepairTimeout: "10ms"}, DeviceConfig{ID: "test", Type: DeviceTypeEmulator}, ioDiscard{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline: %v", err)
	}
}

func TestUntrackedHolderStatusDoesNotReportDeadPID(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "test", Serial: "synthetic", Type: DeviceTypeEmulator}
	if _, _, err := store.acquire(device, 0, time.Hour); err != nil {
		t.Fatal(err)
	}
	app := &App{stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{store: store, backends: newBackendRegistry(Config{}, runnerFunc(func(context.Context, string, ...string) (string, error) { return "device", nil }))}
	if app.status(context.Background(), rt, device).HolderAlive != nil {
		t.Fatal("untracked holder reported as a dead process")
	}
}

func TestGCDoesNotInterruptDevicePreparation(t *testing.T) {
	runner := newSimulatorRunner(map[string]string{"SIM-1": "Shutdown", "SIM-2": "Shutdown"})
	runner.bootStarted, runner.allowBoot = make(chan struct{}), make(chan struct{})
	app, rt := capacityTestApp(t, runner)
	done := make(chan error, 1)
	go func() {
		_, _, err := app.checkout(context.Background(), rt, checkoutOptions{
			platform: PlatformIOS, deviceType: DeviceTypeSimulator, ttl: time.Millisecond,
		})
		done <- err
	}()
	<-runner.bootStarted
	time.Sleep(10 * time.Millisecond)
	if reaped, retained := app.gc(context.Background(), rt, nil); reaped != 0 || retained != 0 {
		t.Errorf("gc interrupted preparation: %d,%d", reaped, retained)
	}
	close(runner.allowBoot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runner.state("SIM-1") != "Booted" {
		t.Fatal("preparing simulator was shut down")
	}
}
