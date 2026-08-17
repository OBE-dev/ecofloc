package cpubpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel cpuBPF cpu_bpf.c

// The line above compiles cpu_bpf.c with clang and generates
// the cpubpf_bpfel.o object file and the cpubpf_bpfel.go binding
// This will be executed when running go generate.

import (
	"fmt"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Loader owns the eBPF objects and the tracepoint attachment for the CPU probe.
type Loader struct {
	objs cpuBPFObjects
	tp   link.Link
}

// NewLoader loads the compiled eBPF objects into the kernel and attaches the
// sched_switch tracepoint. It must be called with sufficient privileges
func NewLoader() (*Loader, error) {
	// Modern kernels use the BPF memcg accounting, but removing the memlock
	// rlimit keeps compatibility with older ones.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("removing memlock rlimit: %w", err)
	}

	var objs cpuBPFObjects
	if err := loadCpuBPFObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("loading eBPF objects: %w", err)
	}

	tp, err := link.Tracepoint("sched", "sched_switch", objs.HandleSchedSwitch, nil)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("attaching sched_switch tracepoint: %w", err)
	}

	return &Loader{objs: objs, tp: tp}, nil
}

// Read returns the current cumulative CPU time in nanoseconds keyed by PID.
// map["pid_key"] = cpu_time_consumed
func (l *Loader) Read() (map[uint32]uint64, error) {
	out := make(map[uint32]uint64)
	var (
		key uint32
		val cpuBPFCpuTimeConsumed
	)
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