package roboranch

import "time"

const (
	exitOK          = 0
	exitUnavailable = 1
	exitUsage       = 2
	exitUnhealthy   = 3
)

const (
	defaultTTL           = 30 * time.Minute
	defaultWait          = 0 * time.Second
	defaultRepairTimeout = 2 * time.Minute
)

type DeviceType string

const (
	DeviceTypeAny      DeviceType = "any"
	DeviceTypeEmulator DeviceType = "emulator"
	DeviceTypePhysical DeviceType = "device"
)

type Config struct {
	Version       int                      `json:"version"`
	StateDir      string                   `json:"stateDir,omitempty"`
	AndroidSDK    string                   `json:"androidSdk,omitempty"`
	DefaultTTL    string                   `json:"defaultTTL,omitempty"`
	RepairTimeout string                   `json:"repairTimeout,omitempty"`
	Backends      map[string]BackendConfig `json:"backends,omitempty"`
	Devices       []DeviceConfig           `json:"devices"`
}

type BackendConfig struct {
	Type string `json:"type"`
}

type DeviceConfig struct {
	ID            string         `json:"id"`
	Type          DeviceType     `json:"type"`
	Serial        string         `json:"serial"`
	Labels        []string       `json:"labels,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	Cleanup       *CleanupConfig `json:"cleanup,omitempty"`
	Backend       string         `json:"backend,omitempty"`
	LaunchdLabel  string         `json:"launchdLabel,omitempty"`
	SystemdUnit   string         `json:"systemdUnit,omitempty"`
	AVD           string         `json:"avd,omitempty"`
	Port          int            `json:"port,omitempty"`
	Snapshot      string         `json:"snapshot,omitempty"`
	LogPath       string         `json:"logPath,omitempty"`
	BootTimeout   string         `json:"bootTimeout,omitempty"`
	EmulatorFlags []string       `json:"emulatorFlags,omitempty"`
}

type CleanupConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (d DeviceConfig) cleanupEnabled() bool {
	if d.Cleanup != nil && d.Cleanup.Enabled != nil {
		return *d.Cleanup.Enabled
	}
	return d.Type == DeviceTypeEmulator
}

type Lease struct {
	LeaseID    string     `json:"lease"`
	ID         string     `json:"id"`
	Serial     string     `json:"serial"`
	Type       DeviceType `json:"type"`
	HolderPID  int        `json:"holderPid"`
	Hostname   string     `json:"hostname"`
	AcquiredAt time.Time  `json:"acquiredAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

type DeviceStatus struct {
	ID          string     `json:"id"`
	Type        DeviceType `json:"type"`
	Serial      string     `json:"serial"`
	Labels      []string   `json:"labels,omitempty"`
	Locked      bool       `json:"locked"`
	Healthy     bool       `json:"healthy"`
	Lease       *Lease     `json:"lease,omitempty"`
	HolderAlive *bool      `json:"holderAlive,omitempty"`
}

type CheckoutResult struct {
	Lease     string     `json:"lease"`
	ID        string     `json:"id"`
	Serial    string     `json:"serial"`
	Type      DeviceType `json:"type"`
	ExpiresAt time.Time  `json:"expiresAt"`
}
