package roboranch

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression tests for the with-lease signal contract.
//
// Before this was fixed, `cmdWithLease` ran the child with `exec.Command` and
// no process group, no signal handling, and no deferred release. `kill -TERM`
// on roboranch therefore killed only roboranch: the child survived orphaned AND
// the lease was never released. Because the holder PID was then dead, the next
// checkout's GC considered the lease stale and handed the device to another
// consumer — while the orphan was still driving it. That is the exact
// double-booking the pool exists to prevent, so it is worth pinning.

// processAliveForTest reports whether a pid is still running. Mirrors the
// production liveness check closely enough for assertions.
func processAliveForTest(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 performs error checking without delivering a signal.
	return syscall.Kill(pid, 0) == nil
}

func waitForExit(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !processAliveForTest(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAliveForTest(pid)
}

func TestSignalProcessGroupTerminatesChildAndGrandchild(t *testing.T) {
	// `sh` backgrounds a sleep (the grandchild) and waits. Only a
	// process-GROUP signal reaches both; signalling the child alone leaves the
	// grandchild orphaned, which is the bug this pins.
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Reap in a goroutine. Without this the child becomes a zombie on exit, and
	// `kill(pid, 0)` succeeds against a zombie — so a liveness probe would
	// report an exited process as still running. (That mistake made an earlier
	// version of this test fail against correct code.)
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		// Non-blocking: the assertion below may already have drained `waited`,
		// and a second receive on a channel that only ever carries one value
		// deadlocks the test binary until the go-test timeout.
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
		}
	})

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("could not read grandchild pid: %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("unexpected grandchild pid %q: %v", line, err)
	}

	if !processAliveForTest(grandchild) {
		t.Fatalf("grandchild %d should be running before the signal", grandchild)
	}

	signalProcessGroup(cmd.Process.Pid, syscall.SIGTERM)

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Errorf("child %d survived a process-group SIGTERM", cmd.Process.Pid)
	}
	// The grandchild is not ours to reap — once sh dies it is reparented to
	// init, which reaps it — so a liveness probe is accurate here.
	if !waitForExit(t, grandchild, 5*time.Second) {
		t.Errorf("grandchild %d was orphaned — the signal did not reach the process group", grandchild)
	}
}

func TestSignalProcessGroupIgnoresZeroSignal(t *testing.T) {
	// Guard against the "signalled" zero value being forwarded as a real
	// signal on the normal, un-interrupted exit path.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessGroup(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	signalProcessGroup(cmd.Process.Pid, 0)

	time.Sleep(200 * time.Millisecond)
	if !processAliveForTest(cmd.Process.Pid) {
		t.Error("signal 0 must be a no-op, but the child exited")
	}
}

func TestSignalProcessGroupSurvivesAlreadyExitedChild(t *testing.T) {
	// Racing a child that exits between select and signal must not panic or
	// error out — release still has to run.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()

	signalProcessGroup(pid, syscall.SIGTERM) // must not panic
}
