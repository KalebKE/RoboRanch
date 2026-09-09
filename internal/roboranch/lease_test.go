package roboranch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
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

func TestIOSBootCapacitySerializesLeasesAndShutdownFreesSlot(t *testing.T) {
	runner := newSimulatorRunner(map[string]string{"SIM-1": "Shutdown", "SIM-2": "Shutdown"})
	app, rt := capacityTestApp(t, runner)
	options := checkoutOptions{
		platform: PlatformIOS, deviceType: DeviceTypeSimulator, ttl: time.Minute, holderPID: os.Getpid(),
	}

	first, code, err := app.checkout(context.Background(), rt, options)
	if err != nil || code != exitOK {
		t.Fatalf("first checkout failed: code=%d err=%v", code, err)
	}
	if runner.bootedCount() != 1 {
		t.Fatalf("expected one booted simulator, got %d", runner.bootedCount())
	}

	if _, code, err = app.checkout(context.Background(), rt, options); err == nil || code != exitUnavailable {
		t.Fatalf("second checkout should be capacity-blocked: code=%d err=%v", code, err)
	}
	if runner.bootedCount() != 1 {
		t.Fatalf("capacity-blocked checkout booted another simulator: %d", runner.bootedCount())
	}

	firstDevice, _ := findDevice(rt.config.Devices, first.ID)
	if err := app.releaseDevice(context.Background(), rt, firstDevice, first.Lease); err != nil {
		t.Fatal(err)
	}
	if runner.bootedCount() != 0 {
		t.Fatalf("release left a simulator booted: %d", runner.bootedCount())
	}

	second, code, err := app.checkout(context.Background(), rt, options)
	if err != nil || code != exitOK {
		t.Fatalf("checkout after release failed: code=%d err=%v", code, err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected least-recently-used rotation, got %s twice", first.ID)
	}
	secondDevice, _ := findDevice(rt.config.Devices, second.ID)
	if err := app.releaseDevice(context.Background(), rt, secondDevice, second.Lease); err != nil {
		t.Fatal(err)
	}
	if runner.bootedCount() != 0 {
		t.Fatalf("final release left a simulator booted: %d", runner.bootedCount())
	}
}

func TestIOSBootCapacityCountsManualSimulatorWithoutStoppingIt(t *testing.T) {
	runner := newSimulatorRunner(map[string]string{
		"SIM-1": "Shutdown", "SIM-2": "Shutdown", "MANUAL": "Booted",
	})
	app, rt := capacityTestApp(t, runner)
	options := checkoutOptions{
		platform: PlatformIOS, deviceType: DeviceTypeSimulator, ttl: time.Minute, holderPID: os.Getpid(),
	}

	if _, code, err := app.checkout(context.Background(), rt, options); err == nil || code != exitUnavailable {
		t.Fatalf("manual simulator should occupy capacity: code=%d err=%v", code, err)
	}
	if runner.state("MANUAL") != "Booted" {
		t.Fatal("capacity enforcement shut down the manual simulator")
	}
	if runner.called("simctl bootstatus") {
		t.Fatalf("capacity enforcement attempted another boot: %v", runner.callList())
	}
}

func TestIOSBootCapacityLockPreventsConcurrentBootRace(t *testing.T) {
	runner := newSimulatorRunner(map[string]string{"SIM-1": "Shutdown", "SIM-2": "Shutdown"})
	runner.bootStarted = make(chan struct{})
	runner.allowBoot = make(chan struct{})
	app, rt := capacityTestApp(t, runner)
	options := checkoutOptions{
		platform: PlatformIOS, deviceType: DeviceTypeSimulator, ttl: time.Minute, holderPID: os.Getpid(),
	}
	type checkoutOutcome struct {
		result CheckoutResult
		code   int
		err    error
	}
	firstDone := make(chan checkoutOutcome, 1)
	go func() {
		result, code, err := app.checkout(context.Background(), rt, options)
		firstDone <- checkoutOutcome{result: result, code: code, err: err}
	}()
	<-runner.bootStarted

	if _, code, err := app.checkout(context.Background(), rt, options); err == nil || code != exitUnavailable {
		t.Fatalf("concurrent checkout should wait on capacity lock: code=%d err=%v", code, err)
	}
	close(runner.allowBoot)
	first := <-firstDone
	if first.err != nil || first.code != exitOK {
		t.Fatalf("first checkout failed: code=%d err=%v", first.code, first.err)
	}
	if runner.bootedCount() != 1 {
		t.Fatalf("concurrent checkout race booted %d simulators", runner.bootedCount())
	}
	device, _ := findDevice(rt.config.Devices, first.result.ID)
	if err := app.releaseDevice(context.Background(), rt, device, first.result.Lease); err != nil {
		t.Fatal(err)
	}
}

func TestIOSStaleLeaseGCShutsDownBeforeUnlocking(t *testing.T) {
	runner := newSimulatorRunner(map[string]string{"SIM-1": "Booted", "SIM-2": "Shutdown"})
	app, rt := capacityTestApp(t, runner)
	device, _ := findDevice(rt.config.Devices, "sim-1")
	if _, ok, err := rt.store.acquire(device, 999999, -time.Minute); err != nil || !ok {
		t.Fatalf("create stale lease: ok=%v err=%v", ok, err)
	}

	reaped, retained := app.gc(context.Background(), rt, nil)
	if reaped != 1 || retained != 0 {
		t.Fatalf("unexpected gc result: reaped=%d retained=%d", reaped, retained)
	}
	if rt.store.locked(device.ID) {
		t.Fatal("gc retained a successfully cleaned lease")
	}
	if runner.state(device.Serial) != "Shutdown" {
		t.Fatal("gc unlocked the lease without shutting down the simulator")
	}
}

func capacityTestApp(t *testing.T, runner CommandRunner) (*App, *runtimeState) {
	t.Helper()
	maxBooted := 1
	shutdown := &CleanupConfig{Mode: string(CleanupShutdown)}
	cfg := Config{
		Version: 1,
		Limits:  LimitsConfig{IOSSimulators: IOSSimulatorLimits{MaxBooted: &maxBooted}},
		Devices: []DeviceConfig{
			{ID: "sim-1", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1", Cleanup: shutdown},
			{ID: "sim-2", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-2", Cleanup: shutdown},
		},
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{runner: runner, stdout: ioDiscard{}, stderr: ioDiscard{}}
	return app, &runtimeState{config: cfg, store: store, backends: newBackendRegistry(cfg, runner)}
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
	if strings.Contains(joined, "logcat -c") || strings.Contains(joined, "am kill-all") {
		t.Fatalf("cleanup erased logs or killed packages without targeting them: %s", joined)
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

type simulatorRunner struct {
	mu          sync.Mutex
	states      map[string]string
	calls       []string
	bootStarted chan struct{}
	allowBoot   chan struct{}
	startOnce   sync.Once
}

func newSimulatorRunner(states map[string]string) *simulatorRunner {
	return &simulatorRunner{states: states}
}

func (f *simulatorRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	joined := strings.Join(args, " ")
	if name == "xcrun" && joined == "simctl list devices --json" {
		f.mu.Lock()
		devices := make([]simctlDevice, 0, len(f.states))
		for udid, state := range f.states {
			devices = append(devices, simctlDevice{Name: udid, UDID: udid, State: state, IsAvailable: true})
		}
		f.mu.Unlock()
		data, err := json.Marshal(simctlDeviceList{Devices: map[string][]simctlDevice{
			"com.apple.CoreSimulator.SimRuntime.iOS-26-4": devices,
		}})
		return string(data), err
	}
	if name == "xcrun" && len(args) >= 4 && args[0] == "simctl" && args[1] == "bootstatus" {
		if f.bootStarted != nil {
			f.startOnce.Do(func() { close(f.bootStarted) })
			<-f.allowBoot
		}
		f.mu.Lock()
		f.states[args[2]] = "Booted"
		f.mu.Unlock()
		return "", nil
	}
	if name == "xcrun" && len(args) == 3 && args[0] == "simctl" && args[1] == "shutdown" {
		f.mu.Lock()
		f.states[args[2]] = "Shutdown"
		f.mu.Unlock()
		return "", nil
	}
	return "", nil
}

func (f *simulatorRunner) bootedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, state := range f.states {
		if state == "Booted" {
			count++
		}
	}
	return count
}

func (f *simulatorRunner) state(udid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[udid]
}

func (f *simulatorRunner) called(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if strings.Contains(call, fragment) {
			return true
		}
	}
	return false
}

func (f *simulatorRunner) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
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
