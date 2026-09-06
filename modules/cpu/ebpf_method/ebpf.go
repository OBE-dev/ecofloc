package ebpf

import (
	"ecofloc/core"
	bpfutils "ecofloc/modules/cpu/ebpf_method/bpf_utils"
	"fmt"
	"os"
	"time"
)

func init() {
	core.RegisterMethod("cpu", "ebpf", EBPFMethodCreator)
}

type ebpfMethod struct {
	cfg          core.Config
	loader       *bpfutils.Loader
	lastReadMap  map[uint32]uint64
	lastReadTime time.Time
	// CpuFeatures can be loaded from cpu.json inside this package.
}

func EBPFMethodCreator(cfg core.Config) (core.Method, error) {
	return &ebpfMethod{
		cfg:         cfg,
		lastReadMap: make(map[uint32]uint64),
	}, nil
}
func (m *ebpfMethod) Start() error {
	l, err := bpfutils.NewLoader(uint32(m.cfg.PID))
	if err != nil {
		return fmt.Errorf("epbf method: %w", err)
	}
	m.loader = l

	// read values from the eBPF map
	base, err := l.Read()
	if err != nil {
		l.Close()
		m.loader = nil
		return fmt.Errorf("epbf method: baseline read: %w", err)
	}
	m.lastReadMap = base
	m.lastReadTime = time.Now()
	return nil
}


// sample returns a sample based on the given parameters
func sample(pid int, deltaCPUTime uint64, elapsedTime float64, ts time.Time) core.Sample {

	// to be done: power := XX
	return core.Sample{
		Module:    "cpu",
		PID:       pid,
		Timestamp: ts,
		Metrics: map[string]float64{
			"cpu_time_ns": float64(deltaCPUTime),
			// to be done "energy_j": power * elapsedTime,
		},
	}
}

func (m *ebpfMethod) Measure() ([]core.Sample, error) {
	if m.loader == nil {
		return nil, fmt.Errorf("epbf method: loader not initialized")
	}

	now := time.Now()
	elapsedtime := now.Sub(m.lastReadTime).Seconds() // elapsed time since last read "real sampling interval"
	if elapsedtime <= 0 {
		// Fallback to the configured sampling cadence if elapsed time is invalid
		elapsedtime = m.cfg.SamplingCadence().Seconds()
	}

	// read current values from the eBPF map
	actualReadMap, err := m.loader.Read()
	if err != nil {
		return nil, fmt.Errorf("epbf method: read: %w", err)
	}

	// delta returns the CPU time a PID consumed since the previous read,
	// the new CPU time should be greater than or equal to the previous CPU time, if not reset the previous CPU time to 0
	delta := func(pid uint32, actualCPUTime uint64) uint64 {
		prevCPUTime := m.lastReadMap[pid]
		if actualCPUTime < prevCPUTime {
			prevCPUTime = 0
		}
		return actualCPUTime - prevCPUTime
	}

	var samples []core.Sample
	if m.cfg.PID > 0 {
		// Single-process measurement: epbf already sum the consumed CPU time for all threads of the target TGID
		if actualCPUTime, exists := actualReadMap[uint32(m.cfg.PID)]; exists {
			samples = append(samples, sample(m.cfg.PID, delta(uint32(m.cfg.PID), actualCPUTime), elapsedtime, now))
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
		samples = append(samples, sample(0, deltaCPUTimeSystem, elapsedtime, now))

	}

	m.lastReadMap = actualReadMap
	m.lastReadTime = now
	return samples, nil
}
func (m *ebpfMethod) Stop() error {
	if m.loader == nil {
		return nil
	}
	// We use the last read map to get the total CPU time
	var total uint64
	if m.cfg.PID > 0 { 
		// Specific PID measurement
		total = m.lastReadMap[uint32(m.cfg.PID)]
	} else { 
		// System-wide measurement
		for _, v := range m.lastReadMap {
			total += v
		}
	}
	fmt.Fprintf(os.Stderr, "CPU: total measured CPU time: %d ns\n", total)
	// Close the eBPF loader
	err := m.loader.Close()
	m.loader = nil
	return err
}