package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// findPIDByName returns the PID of a running process matching name. It matches
// against the process comm (/proc/<pid>/comm) first, then falls back to the
// executable base name from /proc/<pid>/cmdline. If several processes match,
// the lowest PID is returned. It errors if no match is found.
func findPIDByName(name string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("reading /proc: %w", err)
	}

	best := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a PID directory
		}
		if !procMatchesName(pid, name) {
			continue
		}
		if best == 0 || pid < best {
			best = pid
		}
	}

	if best == 0 {
		return 0, fmt.Errorf("no process named %q found", name)
	}
	return best, nil
}

// procMatchesName reports whether the process pid matches name by comm or by
// the base name of its executable.
func procMatchesName(pid int, name string) bool {
	base := fmt.Sprintf("/proc/%d", pid)

	if comm, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		if strings.TrimSpace(string(comm)) == name {
			return true
		}
	}

	if cmdline, err := os.ReadFile(filepath.Join(base, "cmdline")); err == nil && len(cmdline) > 0 {
		// cmdline args are NUL-separated; the first field is the executable.
		exe := string(cmdline)
		if i := strings.IndexByte(exe, 0); i >= 0 {
			exe = exe[:i]
		}
		if filepath.Base(exe) == name {
			return true
		}
	}

	return false
}

// processExists reports whether a process with the given PID is currently
// running, by checking for its /proc entry.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}
