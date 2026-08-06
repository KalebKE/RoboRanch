package roboranch

import (
	"testing"
	"time"
)

// A bare `checkout` prints a lease and exits. Its own PID is therefore dead by the time anyone
// could observe the lease, so treating "holder process gone" as staleness reaped leases the
// instant they were issued -- and the next consumer's checkout runs gc first, so it uninstalled
// the app from a device someone was actively using.
//
// That is not theoretical: a release-gate run installed and warmed the app on emulator-5554,
// then failed at session open with `pm clear com.tracqi.obd2` returning non-zero, with a
// concurrent CI run holding a lease on the same pool. The app was gone.
//
// A lease with no tracked holder is bounded by its TTL alone, which is the honest contract for
// one issued by a CLI that does not stay running.
func TestLeaseWithUntrackedHolderIsNotStaleBeforeItsTTL(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "emu-untracked", Type: DeviceTypeEmulator, Serial: "emulator-5554"}

	if _, ok, err := store.acquire(device, holderPIDUntracked, time.Hour); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected acquire to succeed")
	}

	if stale, reason := store.stale(device.ID, time.Now()); stale {
		t.Fatalf("lease with an untracked holder must not be stale inside its TTL, got %q", reason)
	}
	if got := store.staleLeases(time.Now()); len(got) != 0 {
		t.Fatalf("expected no stale leases, got %v", got)
	}
}

// TTL remains the bound. Without a holder process there is nothing else to notice abandonment,
// so an expired lease must still be reapable or a crashed job would strand the device forever.
func TestLeaseWithUntrackedHolderStillExpires(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "emu-untracked-ttl", Type: DeviceTypeEmulator, Serial: "emulator-5556"}

	if _, ok, err := store.acquire(device, holderPIDUntracked, time.Minute); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected acquire to succeed")
	}

	stale, reason := store.stale(device.ID, time.Now().Add(2*time.Minute))
	if !stale {
		t.Fatal("expected an untracked lease to go stale once its TTL passes")
	}
	if reason != "expired" {
		t.Fatalf("expected reason \"expired\", got %q", reason)
	}
}

// A tracked holder keeps the existing behaviour: `with-lease` stays alive for the duration of the
// child command, so a dead PID there is genuine abandonment and should be reaped promptly rather
// than waiting out a long TTL.
func TestLeaseWithTrackedDeadHolderIsStale(t *testing.T) {
	store, err := newLeaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceConfig{ID: "emu-tracked", Type: DeviceTypeEmulator, Serial: "emulator-5558"}

	// A PID that cannot be alive. Chosen far above any plausible live process rather than
	// spawning and killing one, which would be racy under a busy test runner.
	const deadPID = 0x7FFFFFF0
	if _, ok, err := store.acquire(device, deadPID, time.Hour); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected acquire to succeed")
	}

	stale, reason := store.stale(device.ID, time.Now())
	if !stale {
		t.Fatal("expected a tracked-but-dead holder to be stale")
	}
	if reason == "expired" {
		t.Fatalf("expected a pid-based reason, got %q", reason)
	}
}
