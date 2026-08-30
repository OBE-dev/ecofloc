package cpu

import (
	"ecofloc/core"
	cpubpf "ecofloc/modules/cpu/bpf"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"
)

// the cpu.json is a built-in configuration for the CPU module (embedded during build)
//go:embed cpu.json
var configJSON []byte

// CpuFeatures holds the CPU features read from cpu.json.
type CpuFeatures struct {
	FreqTDP    float64 `json:"cpu_freq_tdp"`
	TDP        float64 `json:"cpu_tdp"`
	VoltageTDP float64 `json:"cpu_voltage_tdp"`
}


type CPU struct {
	config          core.Config  	// system configuration
	features     CpuFeatures   // CPU features
	nproc        int           // number of processors
	loader       *cpubpf.Loader// eBPF loader
	lastReadMap  map[uint32]uint64// mapped values of the last read
	lastReadTime time.Time     // time of the last read
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
	return &CPU{
		config:   cfg,
		features:  feat,
		nproc: runtime.NumCPU(),
		lastReadMap:  make(map[uint32]uint64), // Initialize the map: map[pid]=cputime
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

// Start loads the eBPF program and initializes the last read map/time as a baseline
func (c *CPU) Start() error {
	l, err := cpubpf.NewLoader(uint32(c.config.PID))
	if err != nil {
		return fmt.Errorf("cpu: %w", err)
	}
	c.loader = l

	// read values from the eBPF map
	base, err := l.Read()
	if err != nil {
		l.Close()
		c.loader = nil
		return fmt.Errorf("cpu: baseline read: %w", err)
	}
	c.lastReadMap = base
	c.lastReadTime = time.Now()
	return nil
}

// Measure reads the CPU time consumed by a specified PID or all processes during the last sampling interval
func (c *CPU) Measure() ([]core.Sample, error) {
	if c.loader == nil {
		return nil, fmt.Errorf("cpu: module not started")
	}

	now := time.Now()
	elapsedtime := now.Sub(c.lastReadTime).Seconds() // elapsed time since last read "real sampling interval"
	if elapsedtime <= 0 {
		// Fallback to the configured sampling cadence if elapsed time is invalid
		elapsedtime = c.config.SamplingCadence().Seconds()
	}

	// read current values from the eBPF map
	actualReadMap, err := c.loader.Read()
	if err != nil {
		return nil, err
	}

	// delta returns the CPU time a PID consumed since the previous read,
	// the new CPU time should be greater than or equal to the previous CPU time, if not reset the previous CPU time to 0
	delta := func(pid uint32, actualCPUTime uint64) uint64 {
		prevCPUTime := c.lastReadMap[pid]
		if actualCPUTime < prevCPUTime {
			prevCPUTime = 0
		}
		return actualCPUTime - prevCPUTime
	}

	var samples []core.Sample
	if c.config.PID > 0 {
		// Single-process measurement: epbf already sum the consumed CPU time for all threads of the target TGID
		if actualCPUTime, exists := actualReadMap[uint32(c.config.PID)]; exists {
			samples = append(samples, c.sample(c.config.PID, delta(uint32(c.config.PID), actualCPUTime), elapsedtime, now))
		}
	} else {
		// System-wide measurement: sum every process into a single sample
		var deltaCPUTimeSystem uint64
		for pid, actualCPUTime := range actualReadMap {
			// Skip the idle process (PID 0)
			if pid == 0 {
				continue
			}
			deltaCPUTimeSystem += delta(pid, actualCPUTime)
		}
		samples = append(samples, c.sample(0, deltaCPUTimeSystem, elapsedtime, now))

	}

	c.lastReadMap = actualReadMap
	c.lastReadTime = now
	return samples, nil
}

// sample returns a sample based on the given parameters
func (c *CPU) sample(pid int, deltaCPUTime uint64, elapsedTime float64, ts time.Time) core.Sample {

	// to be done: power := XX
	return core.Sample{
		Module:    c.Name(),
		PID:       pid,
		Timestamp: ts,
		Metrics: map[string]float64{
			"cpu_time_ns": float64(deltaCPUTime),
			// to be done "energy_j": power * elapsedTime,
		},
	}
}

// Stop stops the CPU module
func (c *CPU) Stop() error {
	if c.loader == nil {
		return nil
	}
	// We use the last read map to get the total CPU time
	var total uint64
	if c.config.PID > 0 { 
		// Specific PID measurement
		total = c.lastReadMap[uint32(c.config.PID)]
	} else { 
		// System-wide measurement
		for _, v := range c.lastReadMap {
			total += v
		}
	}
	fmt.Fprintf(os.Stderr, "CPU: total measured CPU time: %d ns\n", total)
	// Close the eBPF loader
	err := c.loader.Close()
	c.loader = nil
	return err
}