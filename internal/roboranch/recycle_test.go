package roboranch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A simulator that is never erased is also never refreshed, and CoreSimulator
// wears out: after ~350 launch/teardown cycles on one booted device the runner
// could no longer install or launch, reporting `Mach error -308 (ipc/mig)
// server died` (tracqi-ios#1154). A shutdown and boot fixed it immediately.
//
// Erase would fix it too, and that is exactly why erase-on-release is disabled
// for the capcom pool: it wipes the installed app that the prepare-once model
// depends on. Recycling is the part of cleanup that is safe to run there.

func TestSimulatorRecycleRebootsWithoutErasing(t *testing.T) {
	var calls []string
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	})}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}

	if err := backend.recycle(context.Background(), Config{}, device, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"xcrun simctl shutdown SIM-1",
		"xcrun simctl bootstatus SIM-1 -b",
		// Warming the device the reboot just cooled (tracqi-ios#1154).
		"xcrun simctl listapps SIM-1",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected recycle calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestSimulatorRecycleReportsABootFailure(t *testing.T) {
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "bootstatus") {
			return "", errors.New("boot failed")
		}
		return "", nil
	})}

	err := backend.recycle(context.Background(), Config{}, DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
	}, ioDiscard{})

	if err == nil || !strings.Contains(err.Error(), "boot simulator") {
		t.Fatalf("expected a boot failure, got %v", err)
	}
}

func TestRecycleRejectsAPhysicalDevice(t *testing.T) {
	backend := iosBackend{runner: runnerFunc(func(context.Context, string, ...string) (string, error) {
		t.Fatal("a physical device must not be recycled")
		return "", nil
	})}

	err := backend.recycle(context.Background(), Config{}, DeviceConfig{
		ID: "phone", Platform: PlatformIOS, Type: DeviceTypePhysical, Serial: "0000",
	}, ioDiscard{})

	if err == nil || !strings.Contains(err.Error(), "not a simulator") {
		t.Fatalf("expected a rejection, got %v", err)
	}
}

// MARK: - The cycle counter

func newCycleStore(t *testing.T) *LeaseStore {
	t.Helper()
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func boolPtr(value bool) *bool { return &value }

func intPtr(value int) *int { return &value }

func TestCycleCounterCountsPerDevice(t *testing.T) {
	store := newCycleStore(t)

	if got := store.recordCycle("sim-a"); got != 1 {
		t.Fatalf("first cycle = %d, want 1", got)
	}
	if got := store.recordCycle("sim-a"); got != 2 {
		t.Fatalf("second cycle = %d, want 2", got)
	}
	if got := store.recordCycle("sim-b"); got != 1 {
		t.Fatalf("other device = %d, want 1", got)
	}
}

func TestCycleCounterResets(t *testing.T) {
	store := newCycleStore(t)
	store.recordCycle("sim-a")
	store.recordCycle("sim-a")

	store.resetCycles("sim-a")

	if got := store.recordCycle("sim-a"); got != 1 {
		t.Fatalf("after reset = %d, want 1", got)
	}
}

func TestCycleCounterSurvivesAMissingFile(t *testing.T) {
	store := newCycleStore(t)

	store.resetCycles("never-seen")

	if got := store.recordCycle("never-seen"); got != 1 {
		t.Fatalf("resetting an unknown device broke the count: %d", got)
	}
}

// MARK: - When a recycle is due

func TestRecycleIsDueAtTheThreshold(t *testing.T) {
	device := DeviceConfig{
		ID: "sim", Type: DeviceTypeSimulator,
		Cleanup: &CleanupConfig{Enabled: boolPtr(false), RecycleAfter: intPtr(3)},
	}

	if device.recycleDue(2) {
		t.Fatal("two cycles is below the threshold")
	}
	if !device.recycleDue(3) {
		t.Fatal("the third cycle is due")
	}
	if !device.recycleDue(4) {
		t.Fatal("past the threshold is still due")
	}
}

func TestRecycleDefaultsToSomethingWellUnderTheObservedFailure(t *testing.T) {
	device := DeviceConfig{ID: "sim", Type: DeviceTypeSimulator}

	after := device.recycleAfterCycles()

	if after <= 0 {
		t.Fatal("a simulator must recycle by default; that is the whole fix")
	}
	if after >= 350 {
		t.Fatalf("default %d is not under the ~350 cycles that killed CoreSimulator", after)
	}
}

func TestRecycleCanBeTurnedOff(t *testing.T) {
	device := DeviceConfig{
		ID: "sim", Type: DeviceTypeSimulator,
		Cleanup: &CleanupConfig{RecycleAfter: intPtr(0)},
	}

	if device.recycleDue(10_000) {
		t.Fatal("recycleAfter 0 means never")
	}
}

func TestNonSimulatorsNeverRecycle(t *testing.T) {
	for _, deviceType := range []DeviceType{DeviceTypePhysical, DeviceTypeEmulator} {
		device := DeviceConfig{ID: "d", Type: deviceType}
		if device.recycleDue(10_000) {
			t.Fatalf("%s must not recycle", deviceType)
		}
	}
}

// MARK: - The release path

func newRecycleRuntime(t *testing.T, device DeviceConfig, calls *[]string) (*App, *runtimeState) {
	t.Helper()
	runner := runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		return "", nil
	})
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Devices: []DeviceConfig{device}}
	return &App{stderr: ioDiscard{}, runner: runner}, &runtimeState{
		config:   cfg,
		store:    store,
		backends: newBackendRegistry(cfg, runner),
	}
}

func TestReleaseRebootsASimulatorAtItsThreshold(t *testing.T) {
	var calls []string
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		// The prepare-once shape: erase is off because it would wipe the app.
		Cleanup: &CleanupConfig{Enabled: boolPtr(false), RecycleAfter: intPtr(2)},
	}
	app, rt := newRecycleRuntime(t, device, &calls)

	app.maintainAfterRelease(context.Background(), rt, device)
	if len(calls) != 0 {
		t.Fatalf("first release must not reboot: %v", calls)
	}

	app.maintainAfterRelease(context.Background(), rt, device)

	want := []string{
		"xcrun simctl shutdown SIM-1",
		"xcrun simctl bootstatus SIM-1 -b",
		"xcrun simctl listapps SIM-1",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected recycle calls:\n%s", strings.Join(calls, "\n"))
	}
	if strings.Contains(strings.Join(calls, "\n"), "erase") {
		t.Fatal("recycling must never erase: the installed app has to survive")
	}
}

func TestTheCountRestartsAfterAReboot(t *testing.T) {
	var calls []string
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		Cleanup: &CleanupConfig{Enabled: boolPtr(false), RecycleAfter: intPtr(2)},
	}
	app, rt := newRecycleRuntime(t, device, &calls)

	for range 3 {
		app.maintainAfterRelease(context.Background(), rt, device)
	}

	if got := strings.Count(strings.Join(calls, "\n"), "bootstatus"); got != 1 {
		t.Fatalf("three releases at a threshold of two rebooted %d times, want 1", got)
	}
}

func TestAnErasingSimulatorJustForgetsItsCount(t *testing.T) {
	var calls []string
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		Cleanup: &CleanupConfig{Enabled: boolPtr(true), RecycleAfter: intPtr(1)},
	}
	app, rt := newRecycleRuntime(t, device, &calls)

	app.maintainAfterRelease(context.Background(), rt, device)
	app.maintainAfterRelease(context.Background(), rt, device)

	if len(calls) != 0 {
		t.Fatalf("cleanup already erases and boots; no extra reboot expected: %v", calls)
	}
}

func TestAFailedRecycleDoesNotBlockTheRelease(t *testing.T) {
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		Cleanup: &CleanupConfig{Enabled: boolPtr(false), RecycleAfter: intPtr(1)},
	}
	runner := runnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "bootstatus") {
			return "", errors.New("boot failed")
		}
		return "", nil
	})
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Devices: []DeviceConfig{device}}
	app := &App{stderr: ioDiscard{}, runner: runner}
	rt := &runtimeState{config: cfg, store: store, backends: newBackendRegistry(cfg, runner)}

	app.maintainAfterRelease(context.Background(), rt, device)

	// The count is kept, so the next release tries again rather than waiting
	// another full threshold on a simulator that is already worn.
	if got := store.recordCycle(device.ID); got != 2 {
		t.Fatalf("count after a failed recycle = %d, want it kept at 2", got)
	}
}

// MARK: - Warming the device the reboot just cooled

// A recycled simulator is handed over cold, and the first XCUITest host launch
// pays for that: across two conformance batteries the first request after a
// reboot timed out on 2 of 8 recycles, recovering on the next run
// (tracqi-ios#1154). `bootstatus -b` waits for the boot to finish, not for the
// device to be useful.

func TestRecycleWarmsTheDeviceAfterBooting(t *testing.T) {
	var calls []string
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	})}
	device := DeviceConfig{ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1"}

	if err := backend.recycle(context.Background(), Config{}, device, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "simctl listapps SIM-1") {
		t.Fatalf("a recycled device must be warmed before it is handed back:\n%s", joined)
	}
	bootIndex := strings.Index(joined, "bootstatus")
	warmIndex := strings.Index(joined, "listapps")
	if bootIndex == -1 || warmIndex < bootIndex {
		t.Fatalf("warming must follow the boot, not precede it:\n%s", joined)
	}
	if strings.Contains(joined, "erase") {
		t.Fatal("warming must not erase")
	}
}

func TestRecycleLaunchesTheConfiguredAppToWarmIt(t *testing.T) {
	var calls []string
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	})}
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		Cleanup: &CleanupConfig{Mode: string(CleanupNone), WarmBundleID: "com.tracqi.OBD2"},
	}

	if err := backend.recycle(context.Background(), Config{}, device, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "simctl launch SIM-1 com.tracqi.OBD2") {
		t.Fatalf("a configured bundle id is the real warm-up:\n%s", joined)
	}
	if !strings.Contains(joined, "simctl terminate SIM-1 com.tracqi.OBD2") {
		t.Fatalf("the warm-up launch must not be left running:\n%s", joined)
	}
}

func TestWarmUpFailureLeavesTheRecycleSuccessful(t *testing.T) {
	backend := iosBackend{runner: runnerFunc(func(_ context.Context, _ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "listapps") || strings.Contains(joined, "launch") {
			return "", errors.New("warm failed")
		}
		return "", nil
	})}
	device := DeviceConfig{
		ID: "sim", Platform: PlatformIOS, Type: DeviceTypeSimulator, Serial: "SIM-1",
		Cleanup: &CleanupConfig{Mode: string(CleanupNone), WarmBundleID: "com.tracqi.OBD2"},
	}

	// The device is rebooted and usable; a warm-up is an optimisation, and
	// failing the release over it would strand the lease.
	if err := backend.recycle(context.Background(), Config{}, device, ioDiscard{}); err != nil {
		t.Fatalf("a failed warm-up must not fail the recycle: %v", err)
	}
}
