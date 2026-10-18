// ebpf method for disk energy measurement
//
// System-wide monitoring: account the bytes completed by the disks (attach to block_rq_complete)
// for process monitoring we attach to:
// - submit_bio: account the reads and direct I/O writes submitted by the process
// - writeback_dirty_folio: account the buffered writes when the process dirties the page cache
// - folio_account_cleaned: account the dirty pages the process drops before they are written (cancelled writes)
package ebpf

import (
	"fmt"
	"os"
	"time"

	"ecofloc/core"
	diskbpf "ecofloc/modules/disk/bpf"
	"ecofloc/modules/disk/diskfeatures"
)

// The init function is called when ebpf_method is imported
// It registers the ebpf method
func init() {
	core.RegisterMethod("disk", "metrics", EBPFMethodCreator)
}

type ebpfMethod struct {
	cfg          core.Config
	loader       *diskbpf.Loader
	lastRead     map[string][3]uint64
	lastReadTime time.Time
	totalEnergyJ float64
	features     diskfeatures.Features
}

func EBPFMethodCreator(cfg core.Config) (core.Method, error) {
	// the disk module provides the disk features (from disk.json) through the configuration
	feat, ok := cfg.ModuleFeatures["disk"].(diskfeatures.Features)
	if !ok {
		return nil, fmt.Errorf("ebpf method: disk features not provided by the disk module")
	}
	return &ebpfMethod{
		cfg:      cfg,
		features: feat,
	}, nil
}

func (m *ebpfMethod) Start() error {
	// Initialize the eBPF loader (which loads the eBPF programs)
	loader, err := diskbpf.NewLoader(m.cfg.PID, m.features)
	if err != nil {
		return fmt.Errorf("ebpf method: %w", err)
	}
	m.loader = loader
	
	// Read the initial values from the eBPF map
	m.lastReadTime = time.Now()
	m.lastRead, err = loader.Read()
	if err != nil {
		_ = loader.Close()
		m.loader = nil
		return fmt.Errorf("ebpf method: baseline read: %w", err)
	}
	return nil
}

func (m *ebpfMethod) Measure() ([]core.Sample, error) {
	if m.loader == nil {
		return nil, fmt.Errorf("ebpf method: loader not initialized")
	}

	now := time.Now()
	elapsed := now.Sub(m.lastReadTime).Seconds()
	if elapsed <= 0 {
		elapsed = m.cfg.SamplingCadence().Seconds()
	}

	// Read the cumulative counters for every configured disk
	current, err := m.loader.Read()
	if err != nil {
		return nil, fmt.Errorf("ebpf method: read: %w", err)
	}

	var totalPowerW float64
	// We calculate the total power over all disks
	for name, counters := range current {
		prev := m.lastRead[name]
		// Counters are cumulative; if they decrease (e.g. map was reset), start from 0
		if counters[0] < prev[0] { //read
			prev[0] = 0
		}
		if counters[1] < prev[1] { //write
			prev[1] = 0
		}
		if counters[2] < prev[2] { //cancelled write
			prev[2] = 0
		}

		readDelta := counters[0] - prev[0]
		writeDelta := counters[1] - prev[1]

		// rectify the written bytes with the dirty pages dropped before being written,
		// they may have been dirtied in a previous interval
		cancelledDelta := counters[2] - prev[2]
		if cancelledDelta < writeDelta {
			writeDelta -= cancelledDelta
		} else {
			writeDelta = 0
		}
		// convert byte deltas over the elapsed time to get KB/s
		readRateKBps := (float64(readDelta) / elapsed) / 1000
		writeRateKBps := (float64(writeDelta) / elapsed) / 1000

		f := m.features[name]
		// calculate read and write power
		readPower := f.ReadPower * (readRateKBps / f.ReadMaxRate)
		writePower := f.WritePower * (writeRateKBps / f.WriteMaxRate)
		avgPower := readPower + writePower
		// Add the power for this disk to the total power
		totalPowerW += avgPower
	}
	// energy over all disks
	energyJ := totalPowerW * elapsed

	// Update the total energy since the start of the measurement
	m.totalEnergyJ += energyJ
	m.lastRead = current
	m.lastReadTime = now

	return []core.Sample{{
		Module:    "disk",
		PID:       m.cfg.PID,
		Timestamp: now,
		Metrics: map[string]float64{
			"power_w":  totalPowerW,
			"energy_j": energyJ,
		},
	}}, nil
}

func (m *ebpfMethod) Stop() error {
	if m.loader == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "disk: total measured energy: %.3f J\n", m.totalEnergyJ)
	return m.loader.Close()
}
