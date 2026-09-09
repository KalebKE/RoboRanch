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

type Platform string

const (
	PlatformAny     Platform = "any"
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
)

type DeviceType string

const (
	DeviceTypeAny       DeviceType = "any"
	DeviceTypeEmulator  DeviceType = "emulator"
	DeviceTypeSimulator DeviceType = "simulator"
	DeviceTypePhysical  DeviceType = "device"
)

type Config struct {
	Version       int                      `json:"version"`
	StateDir      string                   `json:"stateDir,omitempty"`
	AndroidSDK    string                   `json:"androidSdk,omitempty"`
	DefaultTTL    string                   `json:"defaultTTL,omitempty"`
	RepairTimeout string                   `json:"repairTimeout,omitempty"`
	Limits        LimitsConfig             `json:"limits,omitempty"`
	Backends      map[string]BackendConfig `json:"backends,omitempty"`
	Devices       []DeviceConfig           `json:"devices"`
}

type LimitsConfig struct {
	IOSSimulators IOSSimulatorLimits `json:"iosSimulators,omitempty"`
}

type IOSSimulatorLimits struct {
	MaxBooted *int `json:"maxBooted,omitempty"`
}

type BackendConfig struct {
	Type string `json:"type"`
}

type DeviceConfig struct {
	ID            string         `json:"id"`
	Platform      Platform       `json:"platform,omitempty"`
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
	Enabled *bool  `json:"enabled,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type CleanupMode string

const (
	CleanupNone     CleanupMode = "none"
	CleanupReset    CleanupMode = "reset"
	CleanupShutdown CleanupMode = "shutdown"
)

func (d DeviceConfig) cleanupMode() CleanupMode {
	if d.Cleanup != nil && d.Cleanup.Mode != "" {
		return CleanupMode(d.Cleanup.Mode)
	}
	if d.Cleanup != nil && d.Cleanup.Enabled != nil {
		if *d.Cleanup.Enabled {
			return CleanupReset
		}
		return CleanupNone
	}
	if d.Type == DeviceTypeEmulator || d.Type == DeviceTypeSimulator {
		return CleanupReset
	}
	return CleanupNone
}

func (d DeviceConfig) cleanupEnabled() bool {
	return d.cleanupMode() != CleanupNone
}

type Lease struct {
	LeaseID    string     `json:"lease"`
	ID         string     `json:"id"`
	Platform   Platform   `json:"platform,omitempty"`
	Serial     string     `json:"serial"`
	Type       DeviceType `json:"type"`
	HolderPID  int        `json:"holderPid"`
	Hostname   string     `json:"hostname"`
	AcquiredAt time.Time  `json:"acquiredAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

// LeaseStatus is broker metadata only. Reading it never probes the device.
type LeaseStatus struct {
	ID          string     `json:"id"`
	Platform    Platform   `json:"platform"`
	Type        DeviceType `json:"type"`
	Serial      string     `json:"serial"`
	Labels      []string   `json:"labels,omitempty"`
	Locked      bool       `json:"locked"`
	Lease       *Lease     `json:"lease,omitempty"`
	HolderAlive *bool      `json:"holderAlive,omitempty"`
}

type DeviceStatus struct {
	LeaseStatus
	Healthy      bool   `json:"healthy"`
	HealthReason string `json:"healthReason,omitempty"`
}

type CheckoutResult struct {
	Lease     string     `json:"lease"`
	ID        string     `json:"id"`
	Platform  Platform   `json:"platform"`
	Serial    string     `json:"serial"`
	Type      DeviceType `json:"type"`
	ExpiresAt time.Time  `json:"expiresAt"`
}
