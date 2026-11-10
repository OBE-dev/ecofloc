package gpu

import (
	"fmt"

	"ecofloc/core"

	// blank import triggers init() registration of the nvidia-smi measurement method
	_ "ecofloc/modules/gpu/smi_method"
)

type GPU struct {
	config core.Config // system configuration
	method core.Method // GPU measurement method
}

// init is called when the gpu package is imported, before main runs.
func init() {
	core.RegisterModule("gpu", GPUCreator)
}

// GPUCreator creates a new GPU module instance.
func GPUCreator(cfg core.Config) (core.Module, error) {
	methodName := cfg.MeasurementMethods["gpu"]
	if methodName == "" {
		methodName = "smi" // nvidia-smi is the only supported GPU method for now
	}

	creator, err := core.GetMethodCreator("gpu", methodName)
	if err != nil {
		return nil, err
	}

	method, err := creator(cfg)
	if err != nil {
		return nil, fmt.Errorf("gpu: instantiating method %q: %w", methodName, err)
	}

	return &GPU{
		config: cfg,
		method: method,
	}, nil
}

func (g *GPU) Name() string { return "gpu" }

func (g *GPU) Start() error {
	return g.method.Start()
}

func (g *GPU) Measure() ([]core.Sample, error) {
	return g.method.Measure()
}

func (g *GPU) Stop() error {
	return g.method.Stop()
}
