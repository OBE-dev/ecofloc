package ram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ecofloc/core"
	"ecofloc/modules/ram/ramfeatures"

	// blank import triggers init() registration of the perf measurement method
	_ "ecofloc/modules/ram/perf_method"
)

// defaultFeatures is used when ram.json is missing or incomplete.
var defaultFeatures = ramfeatures.Features{
	LoadEnergyNJ:  6.6,
	StoreEnergyNJ: 8.7,
}

type RAM struct {
	config core.Config // system configuration
	method core.Method // RAM measurement method
}

// init is called when the ram package is imported, before main runs.
func init() {
	core.RegisterModule("ram", RAMCreator)
}

// RAMCreator creates a new RAM module instance.
func RAMCreator(cfg core.Config) (core.Module, error) {
	features, err := loadFeatures()
	if err != nil {
		return nil, fmt.Errorf("ram: loading features: %w", err)
	}
	// Forward the RAM features to the selected measurement method.
	cfg.ModuleFeatures = map[string]any{"ram": features}

	methodName := cfg.MeasurementMethods["ram"]
	if methodName == "" {
		methodName = "metrics" // default RAM measurement method
	}

	creator, err := core.GetMethodCreator("ram", methodName)
	if err != nil {
		return nil, err
	}

	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("ram: instantiating method %q: %w", methodName, err)
	}

	return &RAM{
		config: cfg,
		method: method,
	}, nil
}

// configPath returns the path of ram.json, which should be placed in the same
// directory as the executable.
func configPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "ram.json"), nil
}

// loadFeatures parses ram.json. If a value is missing or invalid (<= 0), it
// falls back to the default individually.
func loadFeatures() (ramfeatures.Features, error) {
	path, err := configPath()
	if err != nil {
		return defaultFeatures, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return defaultFeatures, nil
	}

	var configFile struct {
		Features ramfeatures.Features `json:"features"`
	}
	if err := json.Unmarshal(data, &configFile); err != nil {
		return defaultFeatures, nil
	}

	f := configFile.Features
	if f.LoadEnergyNJ <= 0 {
		f.LoadEnergyNJ = defaultFeatures.LoadEnergyNJ
	}
	if f.StoreEnergyNJ <= 0 {
		f.StoreEnergyNJ = defaultFeatures.StoreEnergyNJ
	}
	return f, nil
}

func (r *RAM) Name() string { return "ram" }

func (r *RAM) Start() error {
	return r.method.Start()
}

func (r *RAM) Measure() ([]core.Sample, error) {
	return r.method.Measure()
}

func (r *RAM) Stop() error {
	return r.method.Stop()
}
