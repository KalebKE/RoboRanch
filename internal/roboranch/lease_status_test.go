package roboranch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaseOnlyStatusDoesNotWaitForDeviceHealth(t *testing.T) {
	config, state := leaseTestPool(t)
	store, err := newLeaseStore(state)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	device := cfg.Devices[0]
	lease, acquired, err := store.acquire(device, 0, time.Hour)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v %v", acquired, err)
	}
	before, err := os.ReadFile(store.leasePath(device.ID))
	if err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	releaseHealth := func() { once.Do(func() { close(unblock) }) }
	defer releaseHealth()
	var calls atomic.Int32
	runner := runnerFunc(func(ctx context.Context, _ string, _ ...string) (string, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-unblock:
		case <-ctx.Done():
		}
		return "", errors.New("device health is unavailable")
	})
	var normalOutput, normalErrors bytes.Buffer
	normal := &App{runner: runner, stdout: &normalOutput, stderr: &normalErrors}
	normalDone := make(chan int, 1)
	go func() {
		normalDone <- normal.execute([]string{"--config", config, "status", "--id", device.ID, "--json"})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("normal status did not reach the device health probe")
	}

	var output, stderr bytes.Buffer
	app := &App{runner: runner, stdout: &output, stderr: &stderr}
	done := make(chan int, 1)
	go func() {
		done <- app.execute([]string{"--config", config, "status", "--lease-only", "--id", device.ID, "--json"})
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("lease-only status exited %d: %s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		releaseHealth()
		<-done
		t.Fatal("lease-only status waited on device health")
	}
	if calls.Load() != 1 {
		t.Fatalf("lease-only status invoked the device backend: %d calls", calls.Load())
	}
	select {
	case <-normalDone:
		t.Fatal("normal health probe unexpectedly completed before being unblocked")
	default:
	}
	var status map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	var current Lease
	if err := json.Unmarshal(status["lease"], &current); err != nil {
		t.Fatal(err)
	}
	if current.LeaseID != lease.LeaseID || string(status["locked"]) != "true" || current.ExpiresAt != lease.ExpiresAt {
		t.Fatal("lease-only status did not preserve exact ownership and expiry")
	}
	for _, field := range []string{"healthy", "healthReason", "holderAlive"} {
		if _, exists := status[field]; exists {
			t.Fatalf("lease-only status fabricated %s", field)
		}
	}
	after, err := os.ReadFile(store.leasePath(device.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("lease-only status modified the lease")
	}
	releaseHealth()
	if code := <-normalDone; code != 0 {
		t.Fatalf("normal status exited %d: %s", code, normalErrors.String())
	}
	if !bytes.Contains(normalOutput.Bytes(), []byte(`"healthy":false`)) {
		t.Fatal("normal status no longer reports device health")
	}
}

func TestLeaseOnlyStatusWithoutLeaseOmitsHealth(t *testing.T) {
	config, _ := leaseTestPool(t)
	var output, stderr bytes.Buffer
	app := &App{runner: runnerFunc(func(context.Context, string, ...string) (string, error) {
		t.Error("lease-only status touched the device")
		return "", errors.New("unexpected device probe")
	}), stdout: &output, stderr: &stderr}
	if code := app.execute([]string{"--config", config, "status", "--lease-only", "--id", "test-1", "--json"}); code != 0 {
		t.Fatalf("status exited %d: %s", code, stderr.String())
	}
	var status map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if string(status["locked"]) != "false" || status["lease"] != nil || status["healthy"] != nil {
		t.Fatalf("unexpected unlocked metadata: %s", output.String())
	}
	output.Reset()
	if code := app.execute([]string{"--config", config, "status", "--lease-only", "--id", "test-1"}); code != 0 {
		t.Fatalf("text status exited %d", code)
	}
	if !bytes.Contains(output.Bytes(), []byte("locked=no")) || bytes.Contains(output.Bytes(), []byte("healthy=")) {
		t.Fatalf("unexpected text metadata: %s", output.String())
	}
}
