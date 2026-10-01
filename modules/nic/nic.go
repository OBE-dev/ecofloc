package nic

import (
	"ecofloc/core"
	"ecofloc/modules/nic/nicfeatures"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	// blank imports trigger init() registration of each method
	_ "ecofloc/modules/nic/ebpf_method"
)

type NIC struct {
	config core.Config // system configuration
	method core.Method // NIC measurement method
}

// init function will be called when the nic package is imported, before the main function.
func init() {
	core.RegisterModule("nic", NICCreator)
}

// NICCreator creates a new NIC module instance.
func NICCreator(cfg core.Config) (core.Module, error) {
	// load the nic features from the nic.json file
	features, err := loadFeatures()
	if err != nil {
		return nil, fmt.Errorf("nic: loading features: %w", err)
	}
	// forward the NIC features (nicfeatures.Features) to the selected measurement
	// method through the configuration
	cfg.ModuleFeatures = map[string]any{"nic": features}

	methodName := cfg.MeasurementMethods["nic"]
	if methodName == "" {
		methodName = "metrics" // "metrics" is the default NIC measurement method
	}

	// get the creator for the selected measurement method
	creator, err := core.GetMethodCreator("nic", methodName)
	if err != nil {
		return nil, err
	}

	// instanciate the selected measurement method
	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("nic: instantiating method %q: %w", methodName, err)
	}

	return &NIC{
		config: cfg,
		method: method,
	}, nil
}

// loadFeatures parses nic.json from the same directory as the executable.
// It returns an error if parsing fails.
func loadFeatures() (nicfeatures.Features, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var configFile struct {
		Features nicfeatures.Features `json:"features"`
	}
	if err := json.Unmarshal(data, &configFile); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	features := configFile.Features
	// if the features map has no interface, return an error
	if len(features) == 0 {
		return nil, fmt.Errorf("%s: no interfaces configured", path)
	}

	// check all the listed interfaces if they have valid values
	for name, cfg := range features {
		if cfg.UploadPower <= 0 {
			return nil, fmt.Errorf("%s: interface %q has invalid upload_power %.3f", path, name, cfg.UploadPower)
		}
		if cfg.DownloadPower <= 0 {
			return nil, fmt.Errorf("%s: interface %q has invalid download_power %.3f", path, name, cfg.DownloadPower)
		}
		if cfg.UploadMaxRate <= 0 {
			return nil, fmt.Errorf("%s: interface %q has invalid upload_max_rate %.3f", path, name, cfg.UploadMaxRate)
		}
		if cfg.DownloadMaxRate <= 0 {
			return nil, fmt.Errorf("%s: interface %q has invalid download_max_rate %.3f", path, name, cfg.DownloadMaxRate)
		}
	}

	return features, nil
}

// configPath returns the path of nic.json, which should be placed in the same
// directory as the executable.
func configPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "nic.json"), nil
}

// Name returns the name of the module.
func (n *NIC) Name() string { return "nic" }

// Start(), Measure(), Stop() are all delegated to the measurement method.
func (n *NIC) Start() error {
	return n.method.Start()
}

func (n *NIC) Measure() ([]core.Sample, error) {
	return n.method.Measure()
}

func (n *NIC) Stop() error {
	return n.method.Stop()
}
