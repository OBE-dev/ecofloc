package nicbpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel nicBPF nic_bpf.c

// The line above compiles nic_bpf.c with clang and generates
// the nicbpf_bpfel.o object file and the nicbpf_bpfel.go binding.
// This will be executed when running "go generate".

import (
	"fmt"
	"net"
	"os"

	"ecofloc/core"
	"ecofloc/modules/nic/nicfeatures"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Loader owns the eBPF objects, cgroup attachment and the traffic accounting map.
type Loader struct {
	objs             nicBPFObjects
	egress           link.Link
	ingress          link.Link
	childCgroup      string
	origCgroup       string
	configuredIfaces map[uint32]string // configured interface interface_index -> interface_name
}

// NewLoader loads the eBPF program and attaches it.
//   - pid == 0: system-wide monitoring (attach to the root cgroup)
//   - pid > 0:  process monitoring (we create a dedicated child cgroup and move the process into it, and
//     attach there.
//
// Features holds the network interfaces to monitor with their features.
func NewLoader(pid int, features nicfeatures.Features) (*Loader, error) {
	// features holds the features of each configured interface (from nic.json)
	// if no interfaces is configured, return an error
	if len(features) == 0 {
		return nil, fmt.Errorf("no network interfaces configured")
	}

	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("removing memlock rlimit: %w", err)
	}

	var objs nicBPFObjects
	// Load the eBPF objects into the kernel
	if err := loadNicBPFObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("loading nic bpf objects: %w", err)
	}

	// Resolve the configured interfaces to their current network interface indexes
	// e.g: ifaces = map[1:"eth0", 2:"wlan0"]
	ifaces, err := resolveInterfaces(features)
	if err != nil {
		objs.Close()
		return nil, err
	}
	if len(ifaces) == 0 {
		objs.Close()
		return nil, fmt.Errorf("none of the configured network interfaces were found")
	}

	l := &Loader{
		objs:             objs,
		configuredIfaces: ifaces,
	}

	// System-wide monitoring: attach to root cgroup
	if pid == 0 {
		if err := l.attachCgroup("/sys/fs/cgroup"); err != nil {
			objs.Close()
			return nil, fmt.Errorf("nic bpf system-wide attach: %w", err)
		}
		return l, nil
	}

	// Per-process monitoring: create a child cgroup
	orig, child, err := core.CreateCgroupChild(pid)
	if err != nil {
		objs.Close()
		return nil, err
	}
	l.origCgroup = orig
	l.childCgroup = child

	// Attach the eBPF program to the child cgroup
	if err := l.attachCgroup(child); err != nil {
		core.CleanupChildCgroup(orig, child)
		objs.Close()
		return nil, fmt.Errorf("nic bpf per-tgid attach: %w", err)
	}

	return l, nil
}

// resolveInterfaces returns a map of interface index to interface name for the configured interfaces by the user.
// If an interface name is listed in the nic.json file but not found in the system, it is reported and skipped.
func resolveInterfaces(features nicfeatures.Features) (map[uint32]string, error) {
	// Get all network interfaces
	all, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing network interfaces: %w", err)
	}

	// create a map of interface name to network interface index
	nameToIdx := make(map[string]uint32, len(all))
	for _, iface := range all {
		nameToIdx[iface.Name] = uint32(iface.Index)
	}

	// Iterate over the configured interface in nic.json
	// and check if they exist in the system
	ifaces := make(map[uint32]string, len(features))
	for name := range features {
		idx, ok := nameToIdx[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: nic: configured interface %q not found - skipping\n", name)
			continue
		}
		ifaces[idx] = name
	}

	// return the map of interface index to interface name
	return ifaces, nil
}

// attachCgroup attaches the eBPF programs (egress and ingress) to the specified cgroup path.
func (l *Loader) attachCgroup(cgroupPath string) error {
	var err error
	// Attach the egress program to the cgroup
	l.egress, err = link.AttachCgroup(link.CgroupOptions{
		Path:    cgroupPath,
		Attach:  ebpf.AttachCGroupInetEgress,
		Program: l.objs.CgCountEgress,
	})
	if err != nil {
		return fmt.Errorf("attaching cgroup egress: %w", err)
	}
	// Attach the ingress program to the cgroup
	l.ingress, err = link.AttachCgroup(link.CgroupOptions{
		Path:    cgroupPath,
		Attach:  ebpf.AttachCGroupInetIngress,
		Program: l.objs.CgCountIngress,
	})
	if err != nil {
		l.egress.Close()
		l.egress = nil
		return fmt.Errorf("attaching cgroup ingress: %w", err)
	}

	return nil
}

// Read returns the cumulative per-interface byte counters as seen by the
// eBPF programs. The returned map is keyed by configured interface name; each
// value holds [rx_bytes, tx_bytes]. Only interfaces listed in nic.json are
// returned.
// return example: map["eth0"] = [100, 200]
func (l *Loader) Read() (map[string][2]uint64, error) {
	out := make(map[string][2]uint64, len(l.configuredIfaces))

	var key uint32
	var val nicBPFNicMetrics
	// iterate over the metrics map returned by the eBPF program
	it := l.objs.NicStats.Iterate()
	for it.Next(&key, &val) {
		// configuredIfaces maps interface index (key) to interface name
		ifaceName, ok := l.configuredIfaces[key]
		if !ok {
			// interfaces which are not configured by the user are ignored
			continue
		}
		out[ifaceName] = [2]uint64{val.RxBytes, val.TxBytes}
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("iterating nic stats: %w", err)
	}

	return out, nil
}

// Close detaches the cgroup programs and cleans up any child cgroup created for a
// per-process measurement.
func (l *Loader) Close() error {
	var firstErr error
	// Close the egress/ingress programs
	if l.egress != nil {
		if err := l.egress.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if l.ingress != nil {
		if err := l.ingress.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// If a child cgroup was created (process monitoring case), clean it up
	if l.childCgroup != "" {
		core.CleanupChildCgroup(l.origCgroup, l.childCgroup)
	}
	// Close the eBPF objects
	if err := l.objs.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

