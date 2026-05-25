package roboranch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func defaultConfigPath() string {
	if configured := os.Getenv("ROBORANCH_CONFIG"); configured != "" {
		return configured
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "roboranch", "pool.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "roboranch", "pool.json")
	}
	return "pool.json"
}

func defaultStateDir() string {
	if configured := os.Getenv("ROBORANCH_STATE_DIR"); configured != "" {
		return configured
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "roboranch")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "roboranch")
	}
	return ".roboranch"
}

func LoadConfig(path string) (Config, string, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	path = expandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, path, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, path, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		return Config{}, path, err
	}
	return cfg, path, nil
}

func (c *Config) applyDefaultsAndValidate() error {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	if c.StateDir == "" {
		c.StateDir = defaultStateDir()
	}
	c.StateDir = expandPath(c.StateDir)
	if c.AndroidSDK == "" {
		c.AndroidSDK = discoverAndroidSDK()
	}
	c.AndroidSDK = expandPath(c.AndroidSDK)
	if c.DefaultTTL == "" {
		c.DefaultTTL = defaultTTL.String()
	}
	if c.RepairTimeout == "" {
		c.RepairTimeout = defaultRepairTimeout.String()
	}
	if ttl, err := parseDuration(c.DefaultTTL, defaultTTL); err != nil {
		return fmt.Errorf("defaultTTL: %w", err)
	} else if ttl <= 0 {
		return fmt.Errorf("defaultTTL must be greater than zero")
	}
	if repairTimeout, err := parseDuration(c.RepairTimeout, defaultRepairTimeout); err != nil {
		return fmt.Errorf("repairTimeout: %w", err)
	} else if repairTimeout <= 0 {
		return fmt.Errorf("repairTimeout must be greater than zero")
	}
	if len(c.Devices) == 0 {
		return errors.New("config must define at least one device")
	}
	seen := map[string]bool{}
	for i := range c.Devices {
		device := &c.Devices[i]
		device.ID = strings.TrimSpace(device.ID)
		device.Serial = strings.TrimSpace(device.Serial)
		if device.ID == "" {
			return fmt.Errorf("devices[%d].id is required", i)
		}
		if seen[device.ID] {
			return fmt.Errorf("duplicate device id %q", device.ID)
		}
		seen[device.ID] = true
		if device.Serial == "" {
			return fmt.Errorf("device %q serial is required", device.ID)
		}
		switch device.Type {
		case DeviceTypeEmulator, DeviceTypePhysical:
		default:
			return fmt.Errorf("device %q type must be emulator or device", device.ID)
		}
		if device.Labels == nil {
			device.Labels = []string{}
		}
		if device.LogPath != "" {
			device.LogPath = expandPath(device.LogPath)
		}
		if device.BootTimeout != "" {
			if _, err := parseDuration(device.BootTimeout, defaultRepairTimeout); err != nil {
				return fmt.Errorf("device %q bootTimeout: %w", device.ID, err)
			}
		}
	}
	return nil
}

func (c Config) adbPath() string {
	if c.AndroidSDK == "" {
		return "adb"
	}
	return filepath.Join(c.AndroidSDK, "platform-tools", "adb")
}

func (c Config) emulatorPath() string {
	if c.AndroidSDK == "" {
		return "emulator"
	}
	return filepath.Join(c.AndroidSDK, "emulator", "emulator")
}

func (c Config) defaultTTLDuration() time.Duration {
	d, _ := parseDuration(c.DefaultTTL, defaultTTL)
	return d
}

func (c Config) repairTimeoutDuration() time.Duration {
	d, _ := parseDuration(c.RepairTimeout, defaultRepairTimeout)
	return d
}

func deviceBootTimeout(c Config, d DeviceConfig) time.Duration {
	if d.BootTimeout == "" {
		return c.repairTimeoutDuration()
	}
	parsed, err := parseDuration(d.BootTimeout, c.repairTimeoutDuration())
	if err != nil {
		return c.repairTimeoutDuration()
	}
	return parsed
}

func parseDuration(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	if allDigits(raw) {
		seconds, err := time.ParseDuration(raw + "s")
		if err != nil {
			return 0, err
		}
		return seconds, nil
	}
	return time.ParseDuration(raw)
}

func allDigits(raw string) bool {
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return raw != ""
}

func expandPath(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func discoverAndroidSDK() string {
	for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", "Android", "sdk")
		case "linux":
			return filepath.Join(home, "Android", "Sdk")
		}
	}
	return ""
}
