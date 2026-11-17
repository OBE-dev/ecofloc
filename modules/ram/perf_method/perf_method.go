// RAM measurement method based on perf_event_open hardware counters.
//
// It counts the CPU PMU events "mem-loads" and "mem-stores" and converts them
// to energy using the coefficients from ram.json.
//
// On hybrid Intel systems (cpu_core + cpu_atom PMUs) it discovers both PMUs and
// opens the events on the cores each PMU covers, so system-wide and per-process
// measurements account for both P-cores and E-cores.
package perfmethod

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"ecofloc/core"
	"ecofloc/modules/ram/ramfeatures"

	"golang.org/x/sys/unix"
)

func init() {
	core.RegisterMethod("ram", "metrics", PerfMethodCreator)
}

type perfMethod struct {
	cfg          core.Config
	features     ramfeatures.Features
	loadFds      []int
	storeFds     []int
	lastLoads    uint64
	lastStores   uint64
	lastReadTime time.Time
	totalEnergyJ float64
}

// PerfMethodCreator creates the RAM perf_event measurement method.
func PerfMethodCreator(cfg core.Config) (core.Method, error) {
	feat, ok := cfg.ModuleFeatures["ram"].(ramfeatures.Features)
	if !ok {
		return nil, fmt.Errorf("perf method: ram features not provided by the ram module")
	}
	return &perfMethod{
		cfg:      cfg,
		features: feat,
	}, nil
}

// pmuInfo describes a CPU PMU and the raw configs for the memory events on it.
type pmuInfo struct {
	name    string
	pmuType uint32
	load    uint64
	store   uint64
}

func (m *perfMethod) Start() error {
	// Discover available PMUs (cpu_core, cpu_atom, etc.)
	pmus, err := discoverPMUs()
	if err != nil {
		return fmt.Errorf("perf method: %w", err)
	}
	// If no PMUs found, return an error
	if len(pmus) == 0 {
		return fmt.Errorf("perf method: no CPU PMUs with mem-loads/mem-stores events found")
	}

	if m.cfg.PID == 0 {
		// System-wide: open one load/store pair on each CPU using the PMU that
		// actually covers that CPU.
		ncpus := runtime.NumCPU()
		// Iterate over each CPU
		for cpu := 0; cpu < ncpus; cpu++ {
			opened := false
			var errs []string
			// Try each PMU until one succeeds, because not all PMUs are available on all CPUs
			for _, pmu := range pmus {
				// use -1 to specify that we want to monitor all processes on this CPU
				lfd, lerr := openPerfEvent(pmu.pmuType, pmu.load, -1, int32(cpu))
				if lerr != nil {
					errs = append(errs, fmt.Sprintf("%s/mem-loads: %v", pmu.name, lerr))
					continue
				}
				sfd, serr := openPerfEvent(pmu.pmuType, pmu.store, -1, int32(cpu))
				if serr != nil {
					_ = unix.Close(lfd)
					errs = append(errs, fmt.Sprintf("%s/mem-stores: %v", pmu.name, serr))
					continue
				}
				m.loadFds = append(m.loadFds, lfd)
				m.storeFds = append(m.storeFds, sfd)
				opened = true
				break
			}
			// If no PMU succeeded for this CPU, close all opened FDs and return an error
			if !opened {
				_ = m.closeFds()
				return fmt.Errorf("perf method: could not open mem-loads/mem-stores on CPU %d: %s", cpu, strings.Join(errs, "; "))
			}
		}
	} else {
		// Per-process: open one load/store pair per discovered PMU. The kernel
		// counts events that occur while the target process runs on cores
		// covered by each PMU.
		var errs []string
		for _, pmu := range pmus {
			// use -1 to specify that we want to monitor this process on all CPUs
			lfd, err := openPerfEvent(pmu.pmuType, pmu.load, int32(m.cfg.PID), -1)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s/mem-loads: %v", pmu.name, err))
				continue
			}
			sfd, err := openPerfEvent(pmu.pmuType, pmu.store, int32(m.cfg.PID), -1)
			if err != nil {
				_ = unix.Close(lfd)
				errs = append(errs, fmt.Sprintf("%s/mem-stores: %v", pmu.name, err))
				continue
			}
			m.loadFds = append(m.loadFds, lfd)
			m.storeFds = append(m.storeFds, sfd)
		}
		if len(m.loadFds) == 0 {
			return fmt.Errorf("perf method: could not open mem-loads/mem-stores for pid %d: %s", m.cfg.PID, strings.Join(errs, "; "))
		}
	}

	// Reset and enable all counters.
	allFds := append([]int(nil), m.loadFds...)
	allFds = append(allFds, m.storeFds...)
	for _, fd := range allFds {
		if err := ioctlNoArg(fd, unix.PERF_EVENT_IOC_RESET); err != nil {
			_ = m.closeFds()
			return fmt.Errorf("perf method: resetting counter: %w", err)
		}
		if err := ioctlNoArg(fd, unix.PERF_EVENT_IOC_ENABLE); err != nil {
			_ = m.closeFds()
			return fmt.Errorf("perf method: enabling counter: %w", err)
		}
	}

	// Baseline reading so the first Measure() delta is correct.
	m.lastReadTime = time.Now()
	loads, stores, err := m.readCounters()
	if err != nil {
		_ = m.Stop()
		return fmt.Errorf("perf method: baseline read: %w", err)
	}
	m.lastLoads = loads
	m.lastStores = stores
	return nil
}

func (m *perfMethod) Measure() ([]core.Sample, error) {
	if len(m.loadFds) == 0 {
		return nil, fmt.Errorf("perf method: counters not initialized")
	}

	now := time.Now()
	elapsed := now.Sub(m.lastReadTime).Seconds()
	if elapsed <= 0 {
		elapsed = m.cfg.SamplingCadence().Seconds()
	}
	// Read the counters
	loads, stores, err := m.readCounters()
	if err != nil {
		return nil, fmt.Errorf("perf method: read: %w", err)
	}

	// Counters are cumulative, guard against resets.
	if loads < m.lastLoads {
		m.lastLoads = 0
	}
	if stores < m.lastStores {
		m.lastStores = 0
	}

	loadDelta := loads - m.lastLoads
	storeDelta := stores - m.lastStores

	// Calculate energy in Joules
	energyJ := (float64(loadDelta)*m.features.LoadEnergyNJ + float64(storeDelta)*m.features.StoreEnergyNJ) / 1e9
	powerW := 0.0
	if elapsed > 0 {
		powerW = energyJ / elapsed
	}

	m.totalEnergyJ += energyJ
	m.lastLoads = loads
	m.lastStores = stores
	m.lastReadTime = now

	return []core.Sample{{
		Module:    "ram",
		PID:       m.cfg.PID,
		Timestamp: now,
		Metrics: map[string]float64{
			"power_w":  powerW,
			"energy_j": energyJ,
		},
	}}, nil
}

func (m *perfMethod) Stop() error {
	if len(m.loadFds) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "RAM: total measured energy: %.3f J\n", m.totalEnergyJ)
	return m.closeFds()
}

// readCounters reads the current values from all load and store file descriptors
// and returns the sum of loads and stores
func (m *perfMethod) readCounters() (loads, stores uint64, err error) {
	for _, fd := range m.loadFds {
		v, rerr := readCounter(fd)
		if rerr != nil {
			return 0, 0, rerr
		}
		loads += v
	}
	for _, fd := range m.storeFds {
		v, rerr := readCounter(fd)
		if rerr != nil {
			return 0, 0, rerr
		}
		stores += v
	}
	return loads, stores, nil
}

// closeFds closes all file descriptors and clears the slices
func (m *perfMethod) closeFds() error {
	allFds := append([]int(nil), m.loadFds...)
	allFds = append(allFds, m.storeFds...)
	var firstErr error
	for _, fd := range allFds {
		if err := unix.Close(fd); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.loadFds = nil
	m.storeFds = nil
	return firstErr
}

// openPerfEvent opens a perf event file descriptor for the given PMU type and config
// and returns the file descriptor
func openPerfEvent(pmuType uint32, config uint64, pid, cpu int32) (int, error) {
	attr := &unix.PerfEventAttr{
		Type:   pmuType,
		Config: config,
		Size:   uint32(unsafe.Sizeof(unix.PerfEventAttr{})),
		Bits:   unix.PerfBitExcludeHv | unix.PerfBitExcludeIdle,
	}
	fd, err := unix.PerfEventOpen(attr, int(pid), int(cpu), -1, unix.PERF_FLAG_FD_CLOEXEC)
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// readCounter reads the current value from a perf event file descriptor
// and returns the value as a uint64
func readCounter(fd int) (uint64, error) {
	var buf [8]byte
	n, err := unix.Read(fd, buf[:])
	if err != nil {
		return 0, err
	}
	if n != 8 {
		return 0, fmt.Errorf("unexpected read size %d", n)
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// ioctlNoArg performs an ioctl to the given file descriptor with no argument
func ioctlNoArg(fd int, req uint) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// discoverPMUs searches sysfs for CPU PMUs that expose mem-loads and
// mem-stores events. On hybrid Intel systems this returns both cpu_core and
// cpu_atom.
func discoverPMUs() ([]pmuInfo, error) {
	base := "/sys/bus/event_source/devices"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("listing PMUs: %w", err)
	}

	var pmus []pmuInfo
	// Iterate over all directories in sys/bus/event_source/devices
	for _, entry := range entries {
		pmuPath := filepath.Join(base, entry.Name())
		info, err := os.Stat(pmuPath)
		// Skip if not a directory or if stat fails
		if err != nil || !info.IsDir() {
			continue
		}
		// Check if events directory exists
		eventsDir := filepath.Join(pmuPath, "events")
		if _, err := os.Stat(eventsDir); err != nil {
			continue
		}
		// Read mem-loads and mem-stores event configurations
		loadConfig, lerr := readEventConfig(filepath.Join(eventsDir, "mem-loads"))
		storeConfig, serr := readEventConfig(filepath.Join(eventsDir, "mem-stores"))
		if lerr != nil || serr != nil {
			continue
		}
		// Read PMU type
		pmuType, err := readPMUType(pmuPath)
		if err != nil {
			continue
		}

		pmus = append(pmus, pmuInfo{
			name:    entry.Name(),
			pmuType: pmuType,
			load:    loadConfig,
			store:   storeConfig,
		})
	}
	return pmus, nil
}

// readPMUType reads the PMU type from the type file in sysfs
func readPMUType(pmuPath string) (uint32, error) {
	data, err := os.ReadFile(filepath.Join(pmuPath, "type"))
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

// readEventConfig reads the event configuration from a sysfs event file
func readEventConfig(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// sysfs event files are fixed-size and may contain trailing null bytes.
	s := strings.TrimSpace(string(data))
	s = strings.Trim(s, "\x00")
	return parseEventConfig(s)
}

func parseEventConfig(s string) (uint64, error) {
	fields := strings.Split(s, ",")
	var event, umask, ldlat uint64
	hasEvent, hasUmask := false, false
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		valStr := strings.TrimSpace(kv[1])
		val, err := strconv.ParseUint(valStr, 0, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing %q: %w", f, err)
		}
		switch key {
		case "event":
			event = val
			hasEvent = true
		case "umask":
			umask = val
			hasUmask = true
		case "ldlat":
			ldlat = val
		}
	}
	if !hasEvent || !hasUmask {
		return 0, fmt.Errorf("incomplete event config %q", s)
	}
	// x86 raw event encoding used by the kernel:
	//   bits 0-7:  event select
	//   bits 8-15: unit mask
	//   bits 32-47: load latency threshold
	return event | (umask << 8) | (ldlat << 32), nil
}
