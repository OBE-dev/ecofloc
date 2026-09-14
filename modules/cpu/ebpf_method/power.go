package ebpf

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"ecofloc/modules/cpu/cpufeatures"
)

// Unlike the first EcoFloc version, which relied on sysfs snapshots and a global
// CPU usage ratio, the V.2 version uses the "sched_stat_runtime" eBPF tracepoint to
// obtain the exact nanoseconds that the monitored workload (a PID or the whole
// system) spent on each virtual CPU during the measurement interval.
//
// From the per-virtual-core runtimes we can get the per-physical-core runtimes
// and the energy consumed by each physical core is computed as:
//
//     E = C_core × V² × f × runtime
//
// where C_core is the physical-core share of the chip capacitance.
// The total energy is the sum of the energy consumed by all physical cores.


type powerModel struct {
	features    cpufeatures.Features
	chipCapacitance float64
	totalVCores int
	realCoreOf  []int // vcore -> physical core id
	lastPowerW  float64
}

// newPowerModel creates a power model and builds the vcore -> physical core map.
func newPowerModel(feat cpufeatures.Features) (*powerModel, error) {
	// Get the number of logical CPUs (virtual cores)
	totalVCores := runtime.NumCPU()
	// Map virtual cores to physical cores
	realCoreOf, err := mapRealCores(totalVCores)
	if err != nil {
		return nil, fmt.Errorf("mapping real cores: %w", err)
	}

	chipCapacitance := (0.7 * feat.TDP) /
		(feat.FreqTDP * feat.VoltageTDP * feat.VoltageTDP)

	return &powerModel{
		features:        feat,
		chipCapacitance: chipCapacitance,
		totalVCores:     totalVCores,
		realCoreOf:      realCoreOf,
	}, nil
}

// energy returns the average power (W) and the energy (J) consumed during an
// interval of elapsedSec seconds, given the CPU time (ns) consumed by the
// monitored workload on each virtual CPU during that interval.
func (pm *powerModel) energy(cpuTimeNs map[uint32]uint64, elapsedSec float64) (powerW, energyJ float64) {
	if elapsedSec <= 0 || len(cpuTimeNs) == 0 {
		return 0, 0
	}
	freqs, err := readFrequencies(pm.totalVCores)
	if err != nil {
		return pm.lastPowerW, pm.lastPowerW * elapsedSec
	}
	volts, err := readVoltages(pm.totalVCores)
	if err != nil {
		return pm.lastPowerW, pm.lastPowerW * elapsedSec
	}

	// physicalCore metrics for a physical core
	type physicalCore struct {
		runTimeSec, voltSum, freqSum float64 // sum of metrics for all virtual cores on this physical core
		n                         int     // number of virtual cores on this physical core
	}
	physicalCores := make(map[int]*physicalCore)

	//iterate over all virtual cores
	for v := 0; v < pm.totalVCores; v++ {
		// get the physical core id for this virtual core
		physicalCoreID := pm.realCoreOf[v]
		if physicalCoreID < 0 || volts[v] <= 0 || freqs[v] <= 0 {
			continue
		}
		phyC := physicalCores[physicalCoreID]
		// if no physical core exists for this core id, create one
		if phyC == nil {
			phyC = &physicalCore{}
			physicalCores[physicalCoreID] = phyC
		}
		// add the metrics for this virtual core to the physical core
		phyC.runTimeSec += float64(cpuTimeNs[uint32(v)]) / 1e9 // runtime in seconds
		phyC.voltSum += volts[v]
		phyC.freqSum += freqs[v]
		phyC.n++
	}

	// We calculate the energy consumed by every physical core
	// Than we compute the Total enery which is the sum of all physical cores energy
	// Total_E = Σ(E_phyC) = Σ(C_chip * V_phyC^2 * f_phyC * runtime_phyC)
	for _, phyC := range physicalCores {
		if phyC.n == 0 || phyC.runTimeSec <= 0 {
			continue
		}
		runTime := phyC.runTimeSec
		if runTime > elapsedSec {
			runTime = elapsedSec
		}
		// we compute the average voltage and frequency for this physical core
		volt := phyC.voltSum / float64(phyC.n)
		freq := phyC.freqSum / float64(phyC.n)
		energyJ += pm.chipCapacitance * volt * volt * freq * runTime
	}

	powerW = energyJ / elapsedSec
	pm.lastPowerW = powerW
	return powerW, energyJ
}

// readFrequencies returns the current scaling frequency in MHz for each vcore
// from sysfs (cpufreq). When cpufreq is not exposed, returns -1 for that vcore.
func readFrequencies(totalVCores int) ([]float64, error) {
	freqs := make([]float64, totalVCores)
	for i := 0; i < totalVCores; i++ {
		path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_cur_freq", i)
		data, err := os.ReadFile(path)
		if err != nil {
			freqs[i] = -1
			continue
		}
		kHz, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil || kHz <= 0 {
			freqs[i] = -1
			continue
		}
		freqs[i] = kHz / 1000.0 // MHz
	}
	return freqs, nil
}

// readVoltages returns the current voltage in volts for each vcore by reading
// MSR 0x198 bits 47:32 and dividing by 8192.
func readVoltages(totalVCores int) ([]float64, error) {
	volts := make([]float64, totalVCores)
	for i := 0; i < totalVCores; i++ {
		val, err := readMSR(i, 0x198)
		if err != nil {
			volts[i] = -1
			continue
		}
		bits := (val >> 32) & 0xFFFF
		volts[i] = float64(bits) / 8192.0 // Adjust voltage according to rdmsr scaling
	}
	return volts, nil
}

// readMSR reads a 64-bit MSR register for the given logical CPU.
func readMSR(cpu int, msr uint32) (uint64, error) {
	path := fmt.Sprintf("/dev/cpu/%d/msr", cpu)
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	if _, err := f.Seek(int64(msr), io.SeekStart); err != nil {
		return 0, err
	}

	var val uint64
	if err := binary.Read(f, binary.LittleEndian, &val); err != nil {
		return 0, err
	}
	return val, nil
}

// mapRealCores parses /proc/cpuinfo to build a vcore -> physical core id map.
func mapRealCores(totalVCores int) ([]int, error) {
	realCoreOf := make([]int, totalVCores)
	for i := range realCoreOf {
		realCoreOf[i] = -1
	}

	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vcore := -1
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "processor") {
			// get the logical CPU number
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					vcore = n
				}
			}
		} else if strings.HasPrefix(line, "core id") {
			// get the physical core id (the core id can be beyond the number of vcores)
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && vcore >= 0 && vcore < totalVCores {
				if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					realCoreOf[vcore] = n
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return realCoreOf, nil
}
