package cpu

import (
	"ecofloc/core"
	_ "embed"
	"encoding/json"
	"fmt"

	// blank imports trigger init() registration of each method
	_ "ecofloc/modules/cpu/ebpf_method"
)

// the cpu.json is a built-in configuration for the CPU module (embedded during build)
//
//go:embed cpu.json
var configJSON []byte

// CpuFeatures holds the CPU features read from cpu.json.
type CpuFeatures struct {
	FreqTDP    float64 `json:"cpu_freq_tdp"`
	TDP        float64 `json:"cpu_tdp"`
	VoltageTDP float64 `json:"cpu_voltage_tdp"`
}

type CPU struct {
	config   core.Config // system configuration
	features CpuFeatures // CPU features
	method   core.Method // CPU measurement method
}

// init function will be called when the cpu package is imported, before the main function
func init() {
	core.RegisterModule("cpu", CPUCreator)
}

// CPUCreator creates a new CPU module instance
func CPUCreator(cfg core.Config) (core.Module, error) {
	feat, err := loadFeatures()
	if err != nil {
		return nil, err
	}
	methodName := cfg.MeasurementMethods["cpu"]
	if methodName == "" {
		methodName = "ebpf" // epbf is default if no measurement method is specified by the user
	}

	creator, err := core.GetMethodCreator("cpu", methodName)
	if err != nil {
		return nil, err
	}

	// instantiate a the selected method measurement
	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("cpu: instantiating method %q: %w", methodName, err)
	}
	return &CPU{
		config:   cfg,
		features: feat,
		method:   method,
	}, nil
}

// loadFeatures parses the embedded cpu.json and returns the cpu features
func loadFeatures() (CpuFeatures, error) {
	var wrapper struct {
		Features CpuFeatures `json:"features"`
	}
	if err := json.Unmarshal(configJSON, &wrapper); err != nil {
		return CpuFeatures{}, fmt.Errorf("parsing cpu.json: %w", err)
	}
	return wrapper.Features, nil
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
