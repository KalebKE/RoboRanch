package roboranch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LeaseStore struct {
	stateDir string
	locksDir string
	usedDir  string
	hostname string
}

func newLeaseStore(stateDir string) (*LeaseStore, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	store := &LeaseStore{
		stateDir: stateDir,
		locksDir: filepath.Join(stateDir, "locks"),
		usedDir:  filepath.Join(stateDir, "last-used"),
		hostname: host,
	}
	if err := os.MkdirAll(store.locksDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "logs"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.usedDir, 0o755); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *LeaseStore) lockPath(id string) string {
	return filepath.Join(s.locksDir, id+".lock")
}

func (s *LeaseStore) leasePath(id string) string {
	return filepath.Join(s.locksDir, id+".lease.json")
}

func (s *LeaseStore) usedPath(id string) string {
	return filepath.Join(s.usedDir, id+".used")
}

func (s *LeaseStore) acquire(device DeviceConfig, holderPID int, ttl time.Duration) (*Lease, bool, error) {
	lock := s.lockPath(device.ID)
	if ok, err := s.createLock(lock, holderPID); err != nil {
		return nil, false, err
	} else if !ok {
		if stale, _ := s.stale(device.ID, time.Now()); stale {
			_ = s.forceRelease(device.ID)
			if ok, err := s.createLock(lock, holderPID); err != nil {
				return nil, false, err
			} else if !ok {
				return nil, false, nil
			}
		} else {
			return nil, false, nil
		}
	}
	now := time.Now().UTC()
	lease := &Lease{
		LeaseID:    newLeaseID(),
		ID:         device.ID,
		Serial:     device.Serial,
		Type:       device.Type,
		HolderPID:  holderPID,
		Hostname:   s.hostname,
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
	}
	if err := writeJSONAtomic(s.leasePath(device.ID), lease, 0o644); err != nil {
		_ = os.Remove(lock)
		return nil, false, err
	}
	_ = os.WriteFile(s.usedPath(device.ID), []byte(now.Format(time.RFC3339Nano)+"\n"), 0o644)
	return lease, true, nil
}

func (s *LeaseStore) createLock(path string, holderPID int) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "%d\n", holderPID)
	return err == nil, err
}

func (s *LeaseStore) stale(id string, now time.Time) (bool, string) {
	lease, err := s.readLease(id)
	if err != nil {
		return true, "no lease metadata"
	}
	if lease.ExpiresAt.Before(now.UTC()) {
		return true, "expired"
	}
	if lease.Hostname == s.hostname && !processAlive(lease.HolderPID) {
		return true, fmt.Sprintf("pid %d dead", lease.HolderPID)
	}
	return false, ""
}

func (s *LeaseStore) readLease(id string) (*Lease, error) {
	data, err := os.ReadFile(s.leasePath(id))
	if err != nil {
		return nil, err
	}
	var lease Lease
	if err := json.Unmarshal(data, &lease); err != nil {
		return nil, err
	}
	return &lease, nil
}

func (s *LeaseStore) release(id string, expectedLease string) error {
	if expectedLease != "" {
		lease, err := s.readLease(id)
		if err != nil {
			return err
		}
		if lease.LeaseID != expectedLease {
			return fmt.Errorf("lease mismatch for %s: got %s expected %s", id, lease.LeaseID, expectedLease)
		}
	}
	return s.forceRelease(id)
}

func (s *LeaseStore) forceRelease(id string) error {
	lockErr := os.Remove(s.lockPath(id))
	leaseErr := os.Remove(s.leasePath(id))
	if lockErr != nil && !errors.Is(lockErr, os.ErrNotExist) {
		return lockErr
	}
	if leaseErr != nil && !errors.Is(leaseErr, os.ErrNotExist) {
		return leaseErr
	}
	return nil
}

func (s *LeaseStore) locked(id string) bool {
	_, err := os.Stat(s.lockPath(id))
	return err == nil
}

func (s *LeaseStore) lastUsed(id string) time.Time {
	data, err := os.ReadFile(s.usedPath(id))
	if err == nil {
		if t, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); parseErr == nil {
			return t
		}
	}
	info, err := os.Stat(s.usedPath(id))
	if err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func (s *LeaseStore) orderByLastUsed(devices []DeviceConfig) {
	sort.SliceStable(devices, func(i, j int) bool {
		left := s.lastUsed(devices[i].ID)
		right := s.lastUsed(devices[j].ID)
		if left.Equal(right) {
			return devices[i].ID < devices[j].ID
		}
		if left.IsZero() {
			return true
		}
		if right.IsZero() {
			return false
		}
		return left.Before(right)
	})
}

func (s *LeaseStore) gc(verbose func(string, ...any)) int {
	entries, err := os.ReadDir(s.locksDir)
	if err != nil {
		return 0
	}
	now := time.Now()
	reaped := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".lock")
		if stale, reason := s.stale(id, now); stale {
			_ = s.forceRelease(id)
			reaped++
			if verbose != nil {
				verbose("reaped %s (%s)", id, reason)
			}
		}
	}
	return reaped
}

func writeJSONAtomic(path string, value any, perm os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newLeaseID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=")
	return cmd.Run() == nil
}
