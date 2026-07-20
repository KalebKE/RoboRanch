package roboranch

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLeaseAcquireReleaseAndReacquire(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "emu-1", Type: DeviceTypeEmulator, Serial: "emulator-5554"}
	first, ok, err := store.acquire(device, os.Getpid(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected first acquire to succeed")
	}
	if _, ok, err := store.acquire(device, os.Getpid(), time.Minute); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected second acquire to fail while locked")
	}
	if err := store.release(device.ID, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.acquire(device, os.Getpid(), time.Minute); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected reacquire after release to succeed")
	}
}

func TestLeaseGCReapsExpiredLease(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "emu-1", Type: DeviceTypeEmulator, Serial: "emulator-5554"}
	if _, ok, err := store.acquire(device, os.Getpid(), -time.Second); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected acquire to succeed")
	}
	app := &App{runner: &fakeRunner{states: map[string]string{"emulator-5554": "device"}, packages: map[string][]string{}}, stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, app.runner),
	}
	reaped, retained := app.gc(context.Background(), rt, nil)
	if reaped != 1 {
		t.Fatalf("expected one reaped lease, got %d", reaped)
	}
	if retained != 0 {
		t.Fatalf("expected no retained leases, got %d", retained)
	}
	if store.locked(device.ID) {
		t.Fatal("expected expired lock to be removed")
	}
}

func TestAcquireDoesNotBypassCleanupForStaleLease(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	if _, ok, err := store.acquire(device, os.Getpid(), -time.Second); err != nil || !ok {
		t.Fatalf("initial acquire failed: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.acquire(device, os.Getpid(), time.Minute); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("stale lease must not be reclaimed directly without backend cleanup")
	}
	if !store.locked(device.ID) {
		t.Fatal("stale lease should remain locked until app-level gc performs cleanup")
	}
}

func TestCleanupClaimIsExclusiveAndRecoverable(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.claimCleanup("sim", os.Getpid()); err != nil || !claimed {
		t.Fatalf("expected first cleanup claim: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := store.claimCleanup("sim", os.Getpid()); err != nil {
		t.Fatal(err)
	} else if claimed {
		t.Fatal("expected concurrent cleanup claim to be refused")
	}
	store.clearCleanup("sim")
	if claimed, err := store.claimCleanup("sim", os.Getpid()); err != nil || !claimed {
		t.Fatalf("expected cleanup claim after release: claimed=%v err=%v", claimed, err)
	}
}

func TestCheckoutRotatesTowardLeastRecentlyUsedDevice(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{states: map[string]string{
		"emulator-5554": "device",
		"emulator-5556": "device",
	}}
	app := &App{runner: runner, stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{
		config: Config{
			Version:    1,
			StateDir:   t.TempDir(),
			AndroidSDK: "",
			Devices: []DeviceConfig{
				{ID: "emu-1", Type: DeviceTypeEmulator, Serial: "emulator-5554"},
				{ID: "emu-2", Type: DeviceTypeEmulator, Serial: "emulator-5556"},
			},
		},
		store:    store,
		backends: newBackendRegistry(Config{}, runner),
	}
	options := checkoutOptions{deviceType: DeviceTypeAny, ttl: time.Minute, holderPID: os.Getpid()}
	first, code, err := app.checkout(context.Background(), rt, options)
	if err != nil || code != exitOK {
		t.Fatalf("first checkout failed: code=%d err=%v", code, err)
	}
	if first.ID != "emu-1" {
		t.Fatalf("expected first checkout to use emu-1, got %s", first.ID)
	}
	if err := store.release(first.ID, first.Lease); err != nil {
		t.Fatal(err)
	}
	second, code, err := app.checkout(context.Background(), rt, options)
	if err != nil || code != exitOK {
		t.Fatalf("second checkout failed: code=%d err=%v", code, err)
	}
	if second.ID != "emu-2" {
		t.Fatalf("expected least-recently-used checkout to use emu-2, got %s", second.ID)
	}
}

func TestCleanupUninstallsThirdPartyPackagesWithoutTrimCaches(t *testing.T) {
	runner := &fakeRunner{
		states:   map[string]string{"emulator-5554": "device"},
		packages: map[string][]string{"emulator-5554": {"com.example.test"}},
	}
	adb := newADB("adb", runner)
	adb.cleanup(context.Background(), DeviceConfig{ID: "emu-1", Type: DeviceTypeEmulator, Serial: "emulator-5554"}, ioDiscard{})
	joined := strings.Join(runner.calls, "\n")
	if !strings.Contains(joined, "uninstall com.example.test") {
		t.Fatalf("expected uninstall command, got calls:\n%s", joined)
	}
	if strings.Contains(joined, "trim-caches") {
		t.Fatalf("cleanup must not call pm trim-caches; calls:\n%s", joined)
	}
}

func TestFilterDevicesRequiresAllLabels(t *testing.T) {
	devices := []DeviceConfig{
		{ID: "emu-1", Platform: PlatformAndroid, Type: DeviceTypeEmulator, Serial: "emulator-5554", Labels: []string{"api36", "x86_64"}},
		{ID: "emu-2", Platform: PlatformAndroid, Type: DeviceTypeEmulator, Serial: "emulator-5556", Labels: []string{"api34", "x86_64"}},
	}
	filtered := filterDevices(devices, PlatformAny, DeviceTypeEmulator, "", []string{"api36", "x86_64"})
	if len(filtered) != 1 || filtered[0].ID != "emu-1" {
		t.Fatalf("unexpected filtered result: %#v", filtered)
	}
}

func TestFilterDevicesDefaultsCanKeepAndroidAndIOSSeparate(t *testing.T) {
	devices := []DeviceConfig{
		{ID: "android", Platform: PlatformAndroid, Type: DeviceTypePhysical, Serial: "ANDROID"},
		{ID: "ios", Platform: PlatformIOS, Type: DeviceTypePhysical, Serial: "IOS"},
	}
	android := filterDevices(devices, PlatformAndroid, DeviceTypeAny, "", nil)
	if len(android) != 1 || android[0].ID != "android" {
		t.Fatalf("unexpected Android targets: %#v", android)
	}
	ios := filterDevices(devices, PlatformIOS, DeviceTypeAny, "", nil)
	if len(ios) != 1 || ios[0].ID != "ios" {
		t.Fatalf("unexpected iOS targets: %#v", ios)
	}
}

func TestReleaseValidatesLeaseBeforeIOSCleanup(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	if _, ok, err := store.acquire(device, os.Getpid(), time.Minute); err != nil || !ok {
		t.Fatalf("acquire failed: ok=%v err=%v", ok, err)
	}
	var calls []string
	runner := runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	})
	app := &App{runner: runner, stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, runner),
	}
	err = app.releaseDevice(context.Background(), rt, device, "wrong-lease")
	if err == nil || !strings.Contains(err.Error(), "lease mismatch") {
		t.Fatalf("expected lease mismatch, got %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("cleanup ran before lease validation: %v", calls)
	}
	if !store.locked(device.ID) {
		t.Fatal("mismatched release must retain the lease")
	}
}

func TestIOSCleanupFailureRetainsLease(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}
	lease, ok, err := store.acquire(device, os.Getpid(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire failed: ok=%v err=%v", ok, err)
	}
	runner := runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		if name == "xcrun" && strings.Contains(strings.Join(args, " "), "simctl erase") {
			return "", errors.New("erase failed")
		}
		return "", nil
	})
	app := &App{runner: runner, stdout: ioDiscard{}, stderr: ioDiscard{}}
	rt := &runtimeState{
		config:   Config{Devices: []DeviceConfig{device}},
		store:    store,
		backends: newBackendRegistry(Config{}, runner),
	}
	err = app.releaseDevice(context.Background(), rt, device, lease.LeaseID)
	if err == nil || !strings.Contains(err.Error(), "lease retained") {
		t.Fatalf("expected retained cleanup failure, got %v", err)
	}
	if !store.locked(device.ID) {
		t.Fatal("cleanup failure must retain the lease")
	}
}

func TestEnvironmentWithoutRemovesStalePlatformVariables(t *testing.T) {
	input := []string{
		"PATH=/bin",
		"ANDROID_SERIAL=old",
		"ROBORANCH_TARGET_ID=old-target",
		"KEEP=value",
	}
	got := environmentWithout(input, "ANDROID_SERIAL", "ROBORANCH_TARGET_ID")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "ANDROID_SERIAL") || strings.Contains(joined, "ROBORANCH_TARGET_ID") {
		t.Fatalf("stale variables were retained: %v", got)
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "KEEP=value") {
		t.Fatalf("unrelated variables were removed: %v", got)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

type fakeRunner struct {
	states   map[string]string
	packages map[string][]string
	fail     map[string]error
	calls    []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.fail != nil {
		if err := f.fail[call]; err != nil {
			return "", err
		}
	}
	if name == "adb" && len(args) >= 3 && args[0] == "-s" {
		serial := args[1]
		adbArgs := args[2:]
		if len(adbArgs) == 1 && adbArgs[0] == "get-state" {
			if state := f.states[serial]; state != "" {
				return state + "\n", nil
			}
			return "offline\n", nil
		}
		if strings.Join(adbArgs, " ") == "shell pm list packages -3" {
			var lines []string
			for _, pkg := range f.packages[serial] {
				lines = append(lines, "package:"+pkg)
			}
			return strings.Join(lines, "\n"), nil
		}
		if strings.Join(adbArgs, " ") == "shell getprop sys.boot_completed" {
			if f.states[serial] == "device" {
				return "1\n", nil
			}
			return "", errors.New("not booted")
		}
		return "", nil
	}
	return "", nil
}
