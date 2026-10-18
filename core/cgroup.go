package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GetProcessCgroupPath returns the absolute filesystem path of the cgroup v2 directory
// the process belongs to
func GetProcessCgroupPath(pid int) (string, error) {
	// Read the cgroup file for the process (e.g: returns "0::/user.slice/user-1000.slice/session-1.scope")
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	// check if the cgroup path is in the cgroup2 format (0::xxx)
	// than remove the "0::" prefix and concatenate with "/sys/fs/cgroup"
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "::", 2)
		if len(parts) == 2 && parts[0] == "0" {
			return filepath.Join("/sys/fs/cgroup", strings.TrimSpace(parts[1])), nil
		}
	}

	return "", fmt.Errorf("cgroup v2 path not found for pid %d", pid)
}

// CreateCgroupChild creates a child cgroup under the target process current cgroup
// and moves the process into it.
func CreateCgroupChild(pid int) (origCgroup, childCgroup string, err error) {
	// Get the current cgroup path of the process
	orig, err := GetProcessCgroupPath(pid)
	if err != nil {
		return "", "", fmt.Errorf("finding cgroup of pid %d: %w", pid, err)
	}

	// Create a child cgroup under the current cgroup path
	child := filepath.Join(orig, fmt.Sprintf("ecofloc-%d", pid))
	if err := os.MkdirAll(child, 0755); err != nil {
		return "", "", fmt.Errorf("creating cgroup %s: %w", child, err)
	}

	// Get the path to the cgroup.procs file inside the child cgroup folder
	procs := filepath.Join(child, "cgroup.procs")

	// By writing the pid to the cgroup.procs file, we move the process into the child cgroup we just created
	if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)+"\n"), 0); err != nil {
		_ = os.Remove(child)
		return "", "", fmt.Errorf("moving pid %d into cgroup %s: %w", pid, child, err)
	}

	return orig, child, nil
}

// CleanupChildCgroup moves the monitored process (and its child processes) from
// the created child cgroup back to its original cgroup and then removes the
// child cgroup.
func CleanupChildCgroup(origCgroup, childCgroup string) {
	if origCgroup == "" || childCgroup == "" {
		return
	}

	// get path to cgroup.procs file of the child cgroup
	childProcs := filepath.Join(childCgroup, "cgroup.procs")
	// get path to cgroup.procs file of the original cgroup
	origProcs := filepath.Join(origCgroup, "cgroup.procs")

	// Move all PIDs currently in the child cgroup back to the original cgroup.
	data, err := os.ReadFile(childProcs)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			pid := strings.TrimSpace(line)
			if pid == "" {
				continue
			}
			// By writing the pid to the cgroup.procs file, we move the process back to the original cgroup
			_ = os.WriteFile(origProcs, []byte(pid+"\n"), 0)
		}
	}

	// Remove the child cgroup
	_ = os.Remove(childCgroup)
}