package disk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ecofloc/core"
	"ecofloc/modules/disk/diskfeatures"

	// blank import triggers init() registration of the ebpf measurement method
	_ "ecofloc/modules/disk/ebpf_method"
)

type Disk struct {
	config core.Config // system configuration
	method core.Method // disk measurement method
}

// This function will be called (before the main) when the disk package is imported in the main.go
func init() {
	core.RegisterModule("disk", DiskCreator)
}

// DiskCreator createsa new Disk module instance
func DiskCreator(cfg core.Config) (core.Module, error) {
	features, err := loadFeatures()
	if err != nil {
		return nil, fmt.Errorf("disk: loading features: %w", err)
	}
	// fill the ModuleFeatures map with the disk features inside the global config
	// those features will be used by the selected measurement method
	cfg.ModuleFeatures = map[string]any{"disk": features}

	// get the configured measurement method for the disk
	// if no method is configured, use "metrics" by default
	methodName := cfg.MeasurementMethods["disk"]
	if methodName == "" {
		methodName = "metrics"
	}

	// get the creator for the selected measurement method
	creator, err := core.GetMethodCreator("disk", methodName)
	if err != nil {
		return nil, err
	}

	// instantiate the selected measurement method
	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("disk: instantiating method %q: %w", methodName, err)
	}

	return &Disk{
		config: cfg,
		method: method,
	}, nil
}

// parse the disk.json content, returns the features map, or error if parsing fails
func loadFeatures() (diskfeatures.Features, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var configFile struct {
		Features diskfeatures.Features `json:"features"`
	}
	if err := json.Unmarshal(data, &configFile); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	features := configFile.Features
	// if the features map has no disks, return an error
	if len(features) == 0 {
		return nil, fmt.Errorf("%s: no disks configured", path)
	}

	// check all the listed disks if they have valid values
	for name, cfg := range features {
		if cfg.ReadPower <= 0 {
			return nil, fmt.Errorf("%s: disk %q has invalid read_power %.3f", path, name, cfg.ReadPower)
		}
		if cfg.WritePower <= 0 {
			return nil, fmt.Errorf("%s: disk %q has invalid write_power %.3f", path, name, cfg.WritePower)
		}
		if cfg.ReadMaxRate <= 0 {
			return nil, fmt.Errorf("%s: disk %q has invalid read_max_rate %.3f", path, name, cfg.ReadMaxRate)
		}
		if cfg.WriteMaxRate <= 0 {
			return nil, fmt.Errorf("%s: disk %q has invalid write_max_rate %.3f", path, name, cfg.WriteMaxRate)
		}
	}

	return features, nil
}

//return the path of disk.json, which should be placed 
// in the same directory as the executable
func configPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "disk.json"), nil
}

func (d *Disk) Name() string { return "disk" }

func (d *Disk) Start() error {
	return d.method.Start()
}

func (d *Disk) Measure() ([]core.Sample, error) {
	return d.method.Measure()
}

func (d *Disk) Stop() error {
	return d.method.Stop()
}