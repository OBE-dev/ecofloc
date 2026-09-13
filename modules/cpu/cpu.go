package cpu

import (
	"ecofloc/core"
	"ecofloc/modules/cpu/cpufeatures"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	// blank imports trigger init() registration of each method
	_ "ecofloc/modules/cpu/ebpf_method"
	_ "ecofloc/modules/cpu/rapl_method"
)

type CPU struct {
	config core.Config // system configuration
	method core.Method // CPU measurement method
}

// defaultFeatures is used when cpu.json is missing or incomplete.
var defaultFeatures = cpufeatures.Features{
	FreqTDP:    2800.0,
	TDP:        28.0,
	VoltageTDP: 1.5,
}

// init function will be called when the cpu package is imported, before the main function
func init() {
	core.RegisterModule("cpu", CPUCreator)
}

// CPUCreator creates a new CPU module instance
func CPUCreator(cfg core.Config) (core.Module, error) {
	// forward the CPU features (cpufeatures.Features) to the selected measurement
	// method through the configuration
	cfg.ModuleFeatures = map[string]any{"cpu": loadFeatures()}

	methodName := cfg.MeasurementMethods["cpu"]
	if methodName == "" {
		methodName = "metrics" // metrics is default if no measurement method is specified by the user
	}

	creator, err := core.GetMethodCreator("cpu", methodName)
	if err != nil {
		return nil, err
	}

	// instantiate the selected measurement method
	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("cpu: instantiating method %q: %w", methodName, err)
	}

	return &CPU{
		config: cfg,
		method: method,
	}, nil
}

// configPath returns the path of cpu.json, which should be placed in the same
// directory as the executable.
func configPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "cpu.json"), nil
}

// loadFeatures parses cpu.json. If the file cannot be read or parsed, a warning
// is printed and the default features are returned. A missing or invalid
// (<= 0) value falls back to its default individually.
func loadFeatures() cpufeatures.Features {
	path, err := configPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: cpu:", err, "- using default CPU features")
		return defaultFeatures
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: cpu: reading config file:", err, "- using default CPU features")
		return defaultFeatures
	}

	var configFile struct {
		Features cpufeatures.Features `json:"features"`
	}
	if err := json.Unmarshal(data, &configFile); err != nil {
		fmt.Fprintln(os.Stderr, "warning: cpu: parsing config file:", err, "- using default CPU features")
		return defaultFeatures
	}

	f := configFile.Features
	if f.FreqTDP <= 0 {
		f.FreqTDP = defaultFeatures.FreqTDP
	}
	if f.TDP <= 0 {
		f.TDP = defaultFeatures.TDP
	}
	if f.VoltageTDP <= 0 {
		f.VoltageTDP = defaultFeatures.VoltageTDP
	}
	return f
}

// Name returns the name of the module
func (c *CPU) Name() string { return "cpu" }

// Start(), Measure(), Stop() are all delegated to the measurement method
func (c *CPU) Start() error {
	return c.method.Start()
}

func (c *CPU) Measure() ([]core.Sample, error) {
	return c.method.Measure()
}

func (c *CPU) Stop() error {
	return c.method.Stop()
}
