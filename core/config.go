package core

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)


type Config struct {
	// SamplingTime is the sampling period in milliseconds (-i / "sampling_time").
	// Every module is sampled on this shared cadence. Zero or unset means use default.
	SamplingTime int `json:"sampling_time"`
	// Interval is the total measurement duration in seconds (-t /
	// "measurement_interval"). Zero or unset means run until cancelled.
	Interval int `json:"measurement_interval"`
	// PID restricts measurement to a single process (-p / "pid"). 0 means
	// system-wide monitoring.
	PID int `json:"pid"`
	// AppName, when set, selects the target process by name; its PID is
	// resolved at startup (see ResolveTarget). When set, it overrides PID.
	AppName string `json:"app_name"`
	// list of supported modules, true if enabled, false if disabled (all disabled by default)
	Modules map[string]bool `json:"modules"`
	// list of supported outputs, true if enabled, false if disabled (all disabled by default)
	Outputs map[string]bool `json:"outputs"`
}


func (c *Config) LoadConfigFile(path string) error {
	// Read the system configuration file
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	// Decode the configuration file into the config struct
	if err := json.Unmarshal(data, c); err != nil {
		return fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	return nil
}

// SamplingCadence returns the shared sampling time as a duration.
func (c *Config) SamplingCadence() time.Duration {
	return time.Duration(c.SamplingTime) * time.Millisecond
}

// MeasurementDuration returns the total measurement duration. A zero duration means
// the engine runs until it is interrupted.
func (c *Config) MeasurementDuration() time.Duration {
	if c.Interval <= 0 {
		return 0
	}
	return time.Duration(c.Interval) * time.Second
}

// Validate checks the resolved configuration for obvious mistakes.
func (c Config) Validate() error {
	if c.SamplingTime < 0 {
		return fmt.Errorf("sampling time must be >= 0 (got %d); 0 means use default", c.SamplingTime)
	}
	if c.Interval < 0 {
		return fmt.Errorf("interval must be >= 0 (got %d); 0 means unlimited", c.Interval)
	}
	if c.PID < 0 {
		return fmt.Errorf("pid must be >= 0 (got %d)", c.PID)
	}

	// Check if at least one module is enabled
	noModuleEnabled := true
	for _, isEnabled := range c.Modules {
		if isEnabled {
			noModuleEnabled = false
			break
		}
	}

	if noModuleEnabled {
		return fmt.Errorf("no module enabled: pass --cpu/--ram/... or set them in the config file")
	}
	return nil
}


func (c *Config) ResolveTarget() error {
	// if an app name is provided, find the PID
	if c.AppName != "" {
		pid, err := findPIDByName(c.AppName)
		if err != nil {
			return fmt.Errorf("app %q is not running: %w", c.AppName, err)
		}
		c.PID = pid
		return nil
	}
	// if a PID is provided, check if it exists
	if c.PID != 0 {
		if !processExists(c.PID) {
			return fmt.Errorf("no running process with pid %d", c.PID)
		}
		return nil
	}
	// System-wide monitoring.
	return nil
}