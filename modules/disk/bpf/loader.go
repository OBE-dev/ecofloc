package diskbpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel diskBPF disk_bpf.c

import (
	"fmt"
	"os"

	"ecofloc/modules/disk/diskfeatures"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

// Loader owns the eBPF objects and the eBPF program links.
type Loader struct {
	objs            diskBPFObjects
	links           []link.Link
	configuredDisks map[uint32]string // map[dev_t]disk_name
}

// NewLoader loads the eBPF programs and attaches them to the appropriate hooks.
//   - pid == 0: system-wide monitoring, link to block_rq_complete.
//   - pid > 0:  process monitoring, link to submit_bio, writeback_dirty_folio and, folio_account_cleaned
func NewLoader(pid int, features diskfeatures.Features) (*Loader, error) {
	// features holds the features of each configured disk (from disk.json)
	// if no disks are configured, return an error
	if len(features) == 0 {
		return nil, fmt.Errorf("no disks configured")
	}

	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("removing memlock rlimit: %w", err)
	}

	var objs diskBPFObjects
	// Load the eBPF objects into the kernel
	if err := loadDiskBPFObjects(&objs, nil); err != nil {
		return nil, fmt.Errorf("loading disk bpf objects: %w", err)
	}

	// Resolve the configured disks to their kernel dev_t
	// e.g: disks = map[555:"nvme0n1", 556:"nvme0n2"]
	disks, err := resolveDisks(features)
	if err != nil {
		objs.Close()
		return nil, err
	}
	if len(disks) == 0 {
		objs.Close()
		return nil, fmt.Errorf("none of the configured disks were found")
	}

	l := &Loader{
		objs:            objs,
		configuredDisks: disks,
	}

	// Set the target TGID to monitor inside the eBPF programs (0 = system-wide)
	if err := objs.TargetTgid.Set(uint32(pid)); err != nil {
		objs.Close()
		return nil, fmt.Errorf("setting target tgid: %w", err)
	}

	// System-wide monitoring: account the bytes completed by the disks (attach to block_rq_complete)
	if pid == 0 {
		if err := l.attach("block_rq_complete", objs.HandleBlockRequestComplete, ebpf.AttachTraceRawTp); err != nil {
			l.Close()
			return nil, fmt.Errorf("disk bpf system-wide attach: %w", err)
		}
		return l, nil
	}

	//for process monitoring we attach to:
	// - submit_bio: account the reads and direct I/O writes submitted by the process
	// - writeback_dirty_folio: account the buffered writes when the process dirties the page cache
	// - folio_account_cleaned: account the dirty pages the process drops before they are written (cancelled writes)
	if err := l.attach("submit_bio", objs.HandleSubmitBio, ebpf.AttachTraceFEntry); err != nil {
		l.Close()
		return nil, fmt.Errorf("disk bpf per-process attach: %w", err)
	}
	if err := l.attach("writeback_dirty_folio", objs.HandleDirtyFolio, ebpf.AttachTraceRawTp); err != nil {
		l.Close()
		return nil, fmt.Errorf("disk bpf per-process attach: %w", err)
	}
	if err := l.attach("folio_account_cleaned", objs.HandleFolioCleaned, ebpf.AttachTraceFEntry); err != nil {
		l.Close()
		return nil, fmt.Errorf("disk bpf per-process attach: %w", err)
	}

	return l, nil
}

// attach links a BPF program to its kernel hook.
func (l *Loader) attach(name string, prog *ebpf.Program, attachType ebpf.AttachType) error {
	lk, err := link.AttachTracing(link.TracingOptions{
		Program:    prog,
		AttachType: attachType,
	})
	if err != nil {
		return fmt.Errorf("attaching %s: %w", name, err)
	}
	l.links = append(l.links, lk)
	return nil
}

// Read returns the cumulative per-disk byte counters as
// [read_bytes, write_bytes, cancelled_write_bytes].
func (l *Loader) Read() (map[string][3]uint64, error) {
	out := make(map[string][3]uint64, len(l.configuredDisks))

	var key uint32 // key is the dev_t of the disk
	var val diskBPFDiskMetrics
	it := l.objs.DiskMap.Iterate()
	for it.Next(&key, &val) {
		name, ok := l.configuredDisks[key]
		if !ok {
			continue
		}
		out[name] = [3]uint64{val.ReadBytes, val.WriteBytes, val.CancelledWriteBytes}
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("iterating disk stats: %w", err)
	}

	return out, nil
}

// Close detaches the programs and releases the eBPF objects.
func (l *Loader) Close() error {
	var firstErr error
	for _, lk := range l.links {
		if err := lk.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.links = nil
	if err := l.objs.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// resolveDisks converts configured disk names to the kernel dev_t of the whole disk.
// Partitions are skipped as the eBPF programs account the I/O per whole disk.
func resolveDisks(features diskfeatures.Features) (map[uint32]string, error) {
	out := make(map[uint32]string, len(features))
	for name := range features {
		fmt.Printf("resolving disk: %s\n", name)
		// Check that the configured disk in disk.json is not a partition, if it is, skip it
		if _, err := os.Stat("/sys/class/block/" + name + "/partition"); err == nil {
			fmt.Fprintf(os.Stderr, "warning: disk: configured disk %q is a partition, configure the whole disk instead - skipping\n", name)
			continue
		}
		// Get the device number of the disk
		var st unix.Stat_t
		if err := unix.Stat("/dev/"+name, &st); err != nil {
			fmt.Fprintf(os.Stderr, "warning: disk: configured disk %q not found - skipping\n", name)
			continue
		}
		// get the kernel dev_t from the device number
		dev := kernelDevT(st.Rdev)
		fmt.Printf("resolved disk: %s -> dev_t: %d\n", name, dev)
		out[dev] = name
	}
	return out, nil
}

// kernelDevT converts a userspace device number (major, minor)
// to the kernel internal dev_t
func kernelDevT(rdev uint64) uint32 {
	return unix.Major(rdev)<<20 | unix.Minor(rdev)
}