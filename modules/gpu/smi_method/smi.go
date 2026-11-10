// Package smi implements the GPU measurement method based on nvidia-smi.
// It supports NVIDIA GPUs only and produces power/energy samples from the
// power.draw query. Per-process energy is estimated from the process's SM
// utilization reported by nvidia-smi pmon.
package smi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ecofloc/core"
)

func init() {
	core.RegisterMethod("gpu", "smi", MethodCreator)
}

// smiMethod measures GPU power/energy via nvidia-smi.
type smiMethod struct {
	cfg          core.Config
	gpus         []gpuInfo
	lastMeasure  time.Time
	totalEnergyJ float64
}

// gpuInfo holds the static index of one NVIDIA GPU.
type gpuInfo struct {
	index int
}

// MethodCreator creates a new nvidia-smi measurement method instance.
func MethodCreator(cfg core.Config) (core.Method, error) {
	return &smiMethod{cfg: cfg}, nil
}

// Start verifies that nvidia-smi is available and enumerates the GPUs.
func (m *smiMethod) Start() error {
	// Check if nvidia-smi is available
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return fmt.Errorf("nvidia-smi not found in PATH: %w", err)
	}
	// Enumerate available GPUs
	gpus, err := listGPUs()
	if err != nil {
		return fmt.Errorf("smi: listing GPUs: %w", err)
	}
	// If no GPUs found, return error
	if len(gpus) == 0 {
		return fmt.Errorf("smi: no NVIDIA GPU found")
	}

	m.gpus = gpus
	// Initialize last measure time
	m.lastMeasure = time.Now()
	return nil
}

// Measure reads the current GPU power draw and returns one sample per GPU.
// In per-process mode, the target PID's power consumption is estimated from
// its SM utilization: Pid_power = (Pid_sm_util / 100) * GPU_power
// The SM utilization represents the percentage of time the GPU's streaming
// multiprocessors were actively processing work for the target PID.
func (m *smiMethod) Measure() ([]core.Sample, error) {
	now := time.Now()
	elapsed := now.Sub(m.lastMeasure).Seconds()
	if elapsed <= 0 {
		elapsed = m.cfg.SamplingCadence().Seconds()
	}
	m.lastMeasure = now

	// Get power draw per GPU
	powerByGPU, err := queryPowerDraw()
	if err != nil {
		return nil, err
	}

	var samples []core.Sample

	// In System-wide measurement: report the whole board power/energy per GPU.
	if m.cfg.PID == 0 {
		// Iterate over all GPUs and report power/energy for each
		// And accumulate total energy for the system
		for _, gpu := range m.gpus {
			gpuPowerW := powerByGPU[gpu.index]
			gpuEnergyJ := gpuPowerW * elapsed
			m.totalEnergyJ += gpuEnergyJ
			samples = append(samples, core.Sample{
				Module:    "gpu",
				PID:       0,
				Timestamp: now,
				Metrics: map[string]float64{
					"gpu_index":    float64(gpu.index),
					"gpu_power_w":  gpuPowerW,
					"gpu_energy_j": gpuEnergyJ,
				},
			})
		}
		return samples, nil
	}

	// In Per-process measurement: estimate the consumption based on SM utilization per GPU
	// And the power consumption of that GPU
	utilByGPU, err := queryProcessUtilization(m.cfg.PID)
	if err != nil {
		return nil, err
	}

	for _, gpu := range m.gpus {
		gpuPowerW := powerByGPU[gpu.index]
		smUtil := utilByGPU[gpu.index]
		powerUtilW := gpuPowerW * (smUtil / 100.0)
		energyUtilJ := powerUtilW * elapsed
		m.totalEnergyJ += energyUtilJ
		samples = append(samples, core.Sample{
			Module:    "gpu",
			PID:       m.cfg.PID,
			Timestamp: now,
			Metrics: map[string]float64{
				"gpu_index":         float64(gpu.index),
				"sm_utilization":    smUtil,
				"gpu_power_util_w":  powerUtilW,
				"gpu_energy_util_j": energyUtilJ,
			},
		})
	}

	return samples, nil
}

// Stop prints the total measured energy.
func (m *smiMethod) Stop() error {
	fmt.Fprintf(os.Stderr, "GPU: total measured energy: %.3f J\n", m.totalEnergyJ)
	return nil
}

// listGPUs enumerates the available NVIDIA GPUs.
func listGPUs() ([]gpuInfo, error) {
	out, err := runNvidiaSMI(
		"--query-gpu=index",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(bytes.NewReader(out))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing GPU list: %w", err)
	}

	var gpus []gpuInfo
	for _, rec := range records {
		if len(rec) < 1 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			continue
		}
		gpus = append(gpus, gpuInfo{index: idx})
	}
	return gpus, nil
}

// queryPowerDraw returns the current power draw in Watts for each GPU index.
func queryPowerDraw() (map[int]float64, error) {
	out, err := runNvidiaSMI(
		"--query-gpu=index,power.draw",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(bytes.NewReader(out))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing power.draw: %w", err)
	}

	power := make(map[int]float64)
	for _, rec := range records {
		if len(rec) < 2 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil {
			continue
		}
		val := strings.TrimSpace(rec[1])
		// [N/A] means the reading is unavailable on this GPU.
		if val == "[N/A]" || val == "" {
			power[idx] = 0
			continue
		}
		w, err := strconv.ParseFloat(val, 64)
		if err != nil {
			power[idx] = 0
			continue
		}
		power[idx] = w
	}
	return power, nil
}

// queryProcessUtilization returns the SM utilization percentage for the given
// PID on each GPU index. If the PID is not running on a GPU, the value is 0.
func queryProcessUtilization(pid int) (map[int]float64, error) {
	out, err := runNvidiaSMI("pmon", "-c", "1", "-s", "u")
	if err != nil {
		return nil, err
	}

	util := make(map[int]float64)
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// Minimum fields for pmon -s u: gpu, pid, type, sm, mem, ...
		if len(fields) < 5 {
			continue
		}
		gpuIdx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procPid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// If it's not the process we're looking for, skip it
		if procPid != pid {
			continue
		}
		smVal := fields[3]
		if smVal == "-" {
			continue
		}
		sm, err := strconv.ParseFloat(smVal, 64)
		if err != nil {
			continue
		}
		util[gpuIdx] = sm
	}
	return util, nil
}

// runNvidiaSMI executes nvidia-smi with the provided arguments.
func runNvidiaSMI(args ...string) ([]byte, error) {
	cmd := exec.Command("nvidia-smi", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("nvidia-smi %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("nvidia-smi %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
