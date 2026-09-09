package roboranch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Run the real CLI in a separate, short-lived process, with no shared devices.
func TestLeaseCLIProcess(t *testing.T) {
	if os.Getenv("ROBORANCH_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Execute(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(99)
}

func leaseTestPool(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	sdk := filepath.Join(dir, "sdk")
	adb := filepath.Join(sdk, "platform-tools", "adb")
	if err := os.MkdirAll(filepath.Dir(adb), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n*get-state) echo device ;;\n*dumpsys*) echo mCurrentFocus=Test ;;\n*date*) date -u +%s ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(adb, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "pool.json")
	state := filepath.Join(dir, "state")
	disabled := false
	cfg := Config{Version: 1, StateDir: state, AndroidSDK: sdk, Devices: []DeviceConfig{
		{ID: "test-1", Type: DeviceTypeEmulator, Serial: "synthetic-1", Cleanup: &CleanupConfig{Enabled: &disabled}},
		{ID: "test-2", Type: DeviceTypeEmulator, Serial: "synthetic-2", Cleanup: &CleanupConfig{Enabled: &disabled}},
	}}
	if err := writeJSONAtomic(config, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	return config, state
}

func leaseTestCommand(config string, args ...string) *exec.Cmd {
	argv := append([]string{"-test.run=^TestLeaseCLIProcess$", "--", "--config", config}, args...)
	cmd := exec.Command(os.Args[0], argv...)
	cmd.Env = append(os.Environ(), "ROBORANCH_TEST_CLI=1")
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func TestWithLeaseProcessCompletionAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		signal       syscall.Signal
		exit         int
	}{
		{"success", "echo ready; exit 0", 0, 0},
		{"failure", "echo ready; exit 7", 0, 7},
		{"cancel", "echo ready; exec sleep 60", syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, state := leaseTestPool(t)
			unrelatedOutput, err := leaseTestCommand(config, "checkout", "--serial", "synthetic-2", "--ttl", "45m", "--json").Output()
			if err != nil {
				t.Fatal(err)
			}
			var unrelated CheckoutResult
			if err := json.Unmarshal(unrelatedOutput, &unrelated); err != nil {
				t.Fatal(err)
			}
			cmd := leaseTestCommand(config, "with-lease", "--serial", "synthetic-1", "--ttl", "45m", "--", "sh", "-c", tc.script)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			ready := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				if strings.TrimSpace(line) != "ready" {
					t.Fatalf("child not ready: %q", line)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("child startup timed out")
			}
			if tc.signal != 0 {
				if out, err := leaseTestCommand(config, "gc").CombinedOutput(); err != nil {
					t.Fatalf("gc during child: %v %s", err, out)
				}
				if err := cmd.Process.Signal(tc.signal); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("with-lease did not finish")
			}
			if cmd.ProcessState.ExitCode() != tc.exit {
				t.Fatalf("exit=%d want=%d stderr=%s", cmd.ProcessState.ExitCode(), tc.exit, stderr.String())
			}
			store, err := newLeaseStore(state)
			if err != nil {
				t.Fatal(err)
			}
			if store.locked("test-1") {
				t.Fatal("owned lease survived child completion")
			}
			current, err := store.readLease("test-2")
			if err != nil || current.LeaseID != unrelated.Lease {
				t.Fatalf("unrelated lease changed: %v", err)
			}
			audit, err := os.ReadFile(filepath.Join(state, "logs", "leases.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(audit), unrelated.Lease) || strings.Count(string(audit), `"event":"released"`) != 1 {
				t.Fatalf("unexpected audit: %s", audit)
			}
			if out, err := leaseTestCommand(config, "release", "--id", unrelated.ID, "--lease", unrelated.Lease).CombinedOutput(); err != nil {
				t.Fatalf("unrelated final release: %v %s", err, out)
			}
		})
	}
}

func TestStandaloneCheckoutSurvivesConcurrentGCAndCheckout(t *testing.T) {
	config, state := leaseTestPool(t)
	cmd := leaseTestCommand(config, "checkout", "--serial", "synthetic-1", "--ttl", "45m", "--json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("checkout: %v: %s", err, stderr.String())
	}
	var first CheckoutResult
	if err := json.Unmarshal(data, &first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := leaseTestCommand(config, "gc", "--verbose").CombinedOutput(); err != nil {
				t.Errorf("gc: %v: %s", err, out)
			}
			if out, err := leaseTestCommand(config, "checkout", "--serial", "synthetic-1", "--json").CombinedOutput(); err == nil {
				t.Errorf("competing checkout stole the live lease: %s", out)
			}
		}()
	}
	wg.Wait()
	store, err := newLeaseStore(state)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.readLease(first.ID)
	if err != nil || current.LeaseID != first.Lease {
		t.Fatalf("ownership changed after checkout process exited: lease=%v err=%v", current, err)
	}
	if out, err := leaseTestCommand(config, "release", "--id", first.ID, "--lease", first.Lease).CombinedOutput(); err != nil {
		t.Fatalf("exact release: %v: %s", err, out)
	}
	if store.locked(first.ID) {
		t.Fatal("successful release left a lease")
	}
	if strings.Contains(stderr.String(), first.Lease) {
		t.Fatal("diagnostics exposed the lease token")
	}
}
