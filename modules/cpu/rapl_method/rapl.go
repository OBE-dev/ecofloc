// CPU measurement method based on Intel RAPL energy counters exposed by the Linux powercap framework
// (/sys/class/powercap/intel-rapl:*). It reports the energy actually measured
// by the hardware and is mainly intended as a reference to benchmark other methods.
package rapl

import (
	"ecofloc/core"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func init() {
	core.RegisterMethod("cpu", "rapl", RAPLMethodCreator)
}

const powercapRoot = "/sys/class/powercap"

// preferred RAPL domains, in order: 
// "core": overs the CPU cores only, which is exactly for cpu consumption
// "package-0": used when "core" is not exposed. less precise but still valid
var preferredDomains = []string{"core", "package-0"}

type raplMethod struct {
	cfg          core.Config
	domainName   string
	energyPath   string
	maxEnergyUJ  uint64
	lastEnergyUJ uint64
	lastReadTime time.Time
	lastCPU      cpuTimes // busy CPU time snapshot used for per-PID monitoring
	totalEnergyJ float64
}

// cpuTimes holds the busy CPU time of the target PID and of the
// whole system, read from /proc.
type cpuTimes struct {
	pid   uint64
	total uint64
}

func RAPLMethodCreator(cfg core.Config) (core.Method, error) {
	return &raplMethod{cfg: cfg}, nil
}

func (m *raplMethod) Start() error {
	name, dir, err := findDomain()
	if err != nil {
		return fmt.Errorf("rapl method: %w", err)
	}
	m.domainName = name
	m.energyPath = filepath.Join(dir, "energy_uj")

	m.maxEnergyUJ, err = readUint(filepath.Join(dir, "max_energy_range_uj"))
	if err != nil {
		return fmt.Errorf("rapl method: reading counter range: %w", err)
	}
	// read the initial energy value
	m.lastEnergyUJ, err = readUint(m.energyPath)
	if err != nil {
		return fmt.Errorf("rapl method: reading %s (root required): %w", m.energyPath, err)
	}
	// if a PID is specified, read the initial CPU times
	if m.cfg.PID > 0 {
		if m.lastCPU, err = readCPUTimes(m.cfg.PID); err != nil {
			return fmt.Errorf("rapl method: %w", err)
		}
	}
	m.lastReadTime = time.Now()
	fmt.Fprintf(os.Stderr, "CPU: rapl method using domain %q\n", m.domainName)
	return nil
}

func (m *raplMethod) Measure() ([]core.Sample, error) {
	if m.energyPath == "" {
		return nil, fmt.Errorf("rapl method: not started")
	}

	now := time.Now()
	elapsed := now.Sub(m.lastReadTime).Seconds()
	if elapsed <= 0 {
		elapsed = m.cfg.SamplingCadence().Seconds()
	}
	// read the current energy value (in microjoules)
	energyUJ, err := readUint(m.energyPath)
	if err != nil {
		return nil, fmt.Errorf("rapl method: read: %w", err)
	}
	// the counter wraps around at max_energy_range_uj
	var deltaUJ uint64
	if energyUJ >= m.lastEnergyUJ {
		deltaUJ = energyUJ - m.lastEnergyUJ
	} else { // counter wrapped around
		deltaUJ = m.maxEnergyUJ - m.lastEnergyUJ + energyUJ
	}
	energyJ := float64(deltaUJ) / 1e6

	pid := 0
	if m.cfg.PID > 0 {
		// RAPL is a system-wide measurement. To get the energy consumption for a specific PID,
		// we attribute the energy to the PID proportionally to its share of the busy CPU time during the interval.
		pid = m.cfg.PID
		cur, err := readCPUTimes(pid)
		if err != nil {
			return nil, nil // the process has exited
		}
		pidDelta := cur.pid - m.lastCPU.pid
		totalDelta := cur.total - m.lastCPU.total
		m.lastCPU = cur
		if totalDelta > 0 {
			energyJ *= float64(pidDelta) / float64(totalDelta)
		} else {
			energyJ = 0
		}
	}

	m.lastEnergyUJ = energyUJ
	m.lastReadTime = now
	m.totalEnergyJ += energyJ

	return []core.Sample{{
		Module:    "cpu",
		PID:       pid,
		Timestamp: now,
		Metrics: map[string]float64{
			"power_w":  energyJ / elapsed,
			"energy_j": energyJ,
		},
	}}, nil
}

func (m *raplMethod) Stop() error {
	if m.energyPath == "" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "CPU: total measured energy: %.3f J (rapl %s)\n", m.totalEnergyJ, m.domainName)
	m.energyPath = ""
	return nil
}

// findDomain locates the first preferred RAPL domain under the powercap root and
// returns its name and directory.
func findDomain() (name, dir string, err error) {
	entries, err := filepath.Glob(filepath.Join(powercapRoot, "intel-rapl:*"))
	if err != nil || len(entries) == 0 {
		return "", "", fmt.Errorf("no intel-rapl domain found under %s", powercapRoot)
	}
	found := make(map[string]string)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(e, "name"))
		if err != nil {
			continue
		}
		found[strings.TrimSpace(string(data))] = e
	}
	for _, want := range preferredDomains {
		if dir, ok := found[want]; ok {
			return want, dir, nil
		}
	}
	return "", "", fmt.Errorf("none of the RAPL domains %v is available", preferredDomains)
}

// readUint reads a single unsigned integer from a sysfs/procfs file.
func readUint(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
}

// readCPUTimes returns the busy CPU time of the PID (utime+stime from
// /proc/<pid>/stat) and of the whole system time from the cpu lines in /proc/stat
func readCPUTimes(pid int) (cpuTimes, error) {
	var t cpuTimes

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return t, fmt.Errorf("reading pid %d stat: %w", pid, err)
	}
	// skip the "comm" field, which may contain spaces
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return t, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	// after ")" the fields start at index 2 (state); utime and stime are fields 14 and 15
	if len(fields) < 13 {
		return t, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	t.pid = utime + stime

	// now read the system CPU times
	data, err = os.ReadFile("/proc/stat")
	if err != nil {
		return t, fmt.Errorf("reading /proc/stat: %w", err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields = strings.Fields(line) // cpu user nice system idle iowait irq softirq steal ...
	if len(fields) < 8 || fields[0] != "cpu" {
		return t, fmt.Errorf("malformed /proc/stat")
	}
	for i, f := range fields[1:] {
		if i == 3 || i == 4 { // ignore idle and iowait times
			continue
		}
		v, _ := strconv.ParseUint(f, 10, 64)
		t.total += v
	}
	return t, nil
}
