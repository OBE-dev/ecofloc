package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../../include/bpf -target bpfel kernelBPF kernel_bpf.c

// The line above compiles kernel_bpf.c with clang and generates
// the kernel_bpfel.o object file and the kernel_bpfel.go binding
// This will be executed when running go generate.

import (
	"errors"
	"fmt"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Loader owns the eBPF objects and the tracepoint attachment for the CPU probe.
type Loader struct {
	objs       kernelBPFObjects
	tp         link.Link
	targetTgid uint32 // TGID to monitor; 0 means system-wide monitoring
}

// NewLoader loads the compiled eBPF objects into the kernel and attaches the
// sched_switch tracepoint, and sets the target TGID to monitor inside the eBPF program.
// It must be called with sufficient privileges.
func NewLoader(tgid uint32) (*Loader, error) {
	// Modern kernels use the BPF memcg accounting, but removing the memlock
	// rlimit keeps compatibility with older ones.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("removing memlock rlimit: %w", err)
	}

	var objs kernelBPFObjects
	// Load the eBPF objects into the kernel
	if err := loadKernelBPFObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("loading eBPF objects: %w", err)
	}

	// Set the target TGID to monitor inside the eBPF program
	if err := objs.TargetTgid.Set(tgid); err != nil {
		objs.Close()
		return nil, fmt.Errorf("setting target tgid: %w", err)
	}

	// Attach the sched_switch tracepoint
	tp, err := link.Tracepoint("sched", "sched_switch", objs.HandleSchedSwitch, nil)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("attaching sched_switch tracepoint: %w", err)
	}

	return &Loader{objs: objs, tp: tp, targetTgid: tgid}, nil
}

// Read returns the current cumulative CPU time in nanoseconds keyed by PID.
// map["pid_key"] = cpu_time_consumed
func (l *Loader) Read() (map[uint32]uint64, error) {
	out := make(map[uint32]uint64)

	// If a specific TGID is set, return only that TGID's metrics
	if l.targetTgid != 0 {
		var val kernelBPFCpuTimeConsumed
		//fetch cputime comsumed by the target TGID
		if err := l.objs.CpuTimeNs.Lookup(l.targetTgid, &val); err != nil {
			if errors.Is(err, ebpf.ErrKeyNotExist) {
				out[l.targetTgid] = 0
				return out, nil
			}
			return nil, fmt.Errorf("lookup cpu_time_ns for target %d: %w", l.targetTgid, err)
		}
		out[l.targetTgid] = val.Ns
		return out, nil
	}

	// If no specific TGID is set, return all processes' metrics
	var (
		key uint32
		val kernelBPFCpuTimeConsumed
	)
	// Iterate over all cpu_time_ns entries for all registered processes
	it := l.objs.CpuTimeNs.Iterate()
	for it.Next(&key, &val) {
		out[key] = val.Ns
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("iterating cpu_time_ns map: %w", err)
	}
	return out, nil
}

// Close detaches the tracepoint and releases the eBPF objects.
func (l *Loader) Close() error {
	var firstErr error
	if l.tp != nil {
		if err := l.tp.Close(); err != nil {
			firstErr = err
		}
	}
	if err := l.objs.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
