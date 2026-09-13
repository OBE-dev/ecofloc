// CPU measurement method based on eBPF to track CPU time and power consumption

package ebpf

import (
	"ecofloc/core"
	"ecofloc/modules/cpu/cpufeatures"
	bpfutils "ecofloc/modules/cpu/ebpf_method/bpf_utils"
	"fmt"
	"os"
	"time"
)

func init() {
	core.RegisterMethod("cpu", "metrics", EBPFMethodCreator)
}

type ebpfMethod struct {
	cfg          core.Config
	loader       *bpfutils.Loader
	lastReadMap  map[uint32]uint64
	lastReadTime time.Time
	features     cpufeatures.Features
	power        *powerModel
	totalEnergyJ float64 // energy accumulated over all sampling intervals
}

func EBPFMethodCreator(cfg core.Config) (core.Method, error) {
	// the cpu module provides the CPU features (from cpu.json) through the configuration
	// so read the cpu features from the configuration and be sure it's of type 'Features'
	feat, ok := cfg.ModuleFeatures["cpu"].(cpufeatures.Features)
	if !ok {
		return nil, fmt.Errorf("ebpf method: cpu features not provided by the cpu module")
	}
	return &ebpfMethod{
		cfg:         cfg,
		lastReadMap: make(map[uint32]uint64),
		features:    feat,
	}, nil
}

func (m *ebpfMethod) Start() error {
	// Initialize the power model
	pm, err := newPowerModel(m.features)
	if err != nil {
		return fmt.Errorf("ebpf method: initializing power model: %w", err)
	}
	m.power = pm

	// Initialize the eBPF loader
	l, err := bpfutils.NewLoader(uint32(m.cfg.PID))
	if err != nil {
		return fmt.Errorf("ebpf method: %w", err)
	}
	m.loader = l

	// Read values from the eBPF map
	base, err := l.Read()
	if err != nil {
		l.Close()
		m.loader = nil
		return fmt.Errorf("ebpf method: baseline read: %w", err)
	}
	m.lastReadMap = base
	m.lastReadTime = time.Now()
	return nil
}

// Measure computes the CPU time, power and energy consumed by the monitored
// workload (the target TGID, or the whole system) since the previous call.
func (m *ebpfMethod) Measure() ([]core.Sample, error) {
	if m.loader == nil {
		return nil, fmt.Errorf("ebpf method: loader not initialized")
	}

	now := time.Now()
	elapsedtime := now.Sub(m.lastReadTime).Seconds() // elapsed time since last read "real sampling interval"
	if elapsedtime <= 0 {
		// Fallback to the configured sampling cadence if elapsed time is invalid
		elapsedtime = m.cfg.SamplingCadence().Seconds()
	}

	// read the eBPF map holding the cumulative per-CPU time in nanoseconds consumed on each logical CPU: map[cpu] = ns
	actualReadMap, err := m.loader.Read()
	if err != nil {
		return nil, fmt.Errorf("ebpf method: read: %w", err)
	}

	// CPU time consumed on each CPU since the previous read. The counters are
	// cumulative and should never decrease; if they do, restart from 0.
	deltaPerCPU := make(map[uint32]uint64, len(actualReadMap))
	var totalDeltaTime uint64
	for cpu, actual := range actualReadMap {
		prev := m.lastReadMap[cpu]
		if actual < prev {
			prev = 0
		}
		deltaPerCPU[cpu] = actual - prev
		totalDeltaTime += actual - prev
	}

	// Compute power and energy
	powerW, energyJ := m.power.energy(deltaPerCPU, elapsedtime)
	m.totalEnergyJ += energyJ

	m.lastReadMap = actualReadMap
	m.lastReadTime = now

	return []core.Sample{{
		Module:    "cpu",
		PID:       m.cfg.PID, // 0 in system-wide mode
		Timestamp: now,
		Metrics: map[string]float64{
			"power_w":  powerW,
			"energy_j": energyJ,
		},
	}}, nil
}

func (m *ebpfMethod) Stop() error {
	if m.loader == nil {
		return nil
	}
	// Print total measured energy
	fmt.Fprintf(os.Stderr, "CPU: total measured energy: %.3f J\n", m.totalEnergyJ)
	// Close the eBPF loader
	err := m.loader.Close()
	m.loader = nil
	return err
}
