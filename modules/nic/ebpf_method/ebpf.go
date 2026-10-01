// NIC measurement method based on eBPF cgroup socket buffer programs.
//
// In system-wide mode the BPF programs are attached to the root cgroup
// in the  per process monitoring they are attached to a dedicated child cgroup
// containing the process.
// the BPF programs account bytes per network interface (sent and received)
// Only interfaces configured in nic.json are tracked.

package ebpf

import (
	"fmt"
	"os"
	"time"

	"ecofloc/core"
	nicbpf "ecofloc/modules/nic/bpf"
	"ecofloc/modules/nic/nicfeatures"
)

func init() {
	core.RegisterMethod("nic", "metrics", EBPFMethodCreator)
}

type ebpfMethod struct {
	cfg          core.Config
	loader       *nicbpf.Loader
	lastRead     map[string][2]uint64
	lastReadTime time.Time
	totalEnergyJ float64
	features     nicfeatures.Features
}

func EBPFMethodCreator(cfg core.Config) (core.Method, error) {
	feat, ok := cfg.ModuleFeatures["nic"].(nicfeatures.Features)
	if !ok {
		return nil, fmt.Errorf("ebpf method: nic features not provided by the nic module")
	}
	return &ebpfMethod{
		cfg:      cfg,
		features: feat,
	}, nil
}

func (m *ebpfMethod) Start() error {
	loader, err := nicbpf.NewLoader(m.cfg.PID, m.features)
	if err != nil {
		return fmt.Errorf("ebpf method: %w", err)
	}
	m.loader = loader
	m.lastReadTime = time.Now()
	// Store the baseline cumulative read; Measure() will compute deltas from it.
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

	// Cumulative counters per configured interface name.
	current, err := m.loader.Read()
	if err != nil {
		return nil, fmt.Errorf("ebpf method: read: %w", err)
	}

	var totalPowerW float64

	// We calculate the total power over all interfaces
	for name, counters := range current {
		prev := m.lastRead[name]
		// Counters are cumulative; if they decrease (e.g. map was reset), start from 0.
		if counters[0] < prev[0] { // RX
			prev[0] = 0
		}
		if counters[1] < prev[1] { // TX
			prev[1] = 0
		}
		// Calculate the delta for each counter
		rxDelta := counters[0] - prev[0]
		txDelta := counters[1] - prev[1]
		// Convert byte deltas over the elapsed time to get KB/s
		uploadRateKBps := (float64(txDelta) / elapsed) / 1000
		downloadRateKBps := (float64(rxDelta) / elapsed) / 1000
		// get the features for this interface
		f := m.features[name]
		// Calculate the power for each direction
		uploadPower := (f.UploadPower) * (uploadRateKBps / f.UploadMaxRate)
		downloadPower := (f.DownloadPower) * (downloadRateKBps / f.DownloadMaxRate)
		// Calculate the average power for this interface
		avgPower := uploadPower + downloadPower
		// Add the power for this interface to the total power
		totalPowerW += avgPower
	}
	// energy over all interfaces
	energyJ := totalPowerW * elapsed
 
	// Update the total energy since the start of the measurement
	m.totalEnergyJ += energyJ
	m.lastRead = current
	m.lastReadTime = now

	return []core.Sample{{
		Module:    "nic",
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
	fmt.Fprintf(os.Stderr, "NIC: total measured energy: %.3f J\n", m.totalEnergyJ)
	return m.loader.Close()
}
