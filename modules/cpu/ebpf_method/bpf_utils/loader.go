package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../../include/bpf -target bpfel kernelBPF kernel_bpf.c

// The line above compiles kernel_bpf.c with clang and generates
// the kernel_bpfel.o object file and the kernel_bpfel.go binding
// This will be executed when running go generate.

import (
	"fmt"

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
// sched_stat_runtime tracepoint, and sets the target TGID to monitor inside the eBPF program.
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

	// Attach the sched_stat_runtime tracepoint
	tp, err := link.Tracepoint("sched", "sched_stat_runtime", objs.HandleSchedStatRuntime, nil)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("attaching sched_stat_runtime tracepoint: %w", err)
	}

	return &Loader{objs: objs, tp: tp, targetTgid: tgid}, nil
}

// Read returns the cumulative CPU time in nanoseconds consumed on each logical
// CPU, keyed by CPU id: map[cpu] = ns. 
// In targeted mode it is the time consumed by the target TGID
// otherwise it's the time consumed by all processes.
func (l *Loader) Read() (map[uint32]uint64, error) {
	out := make(map[uint32]uint64)

	var (
		key kernelBPFCpuTimeKey
		val kernelBPFCpuTimeConsumed
	)

	// we iterate over all entries in the map
	// each entry is of from  Map[{tgid, cpu_id}] = cpu_time_ns
	it := l.objs.CpuTimeNs.Iterate()
	for it.Next(&key, &val) {
		// we only keep the entries for the target TGID
		if key.Tgid == l.targetTgid {
			// at the end we get a map of the cpu time consumed by each CPU
			out[key.Cpu] = val.Ns
		}
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
