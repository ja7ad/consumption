//go:build linux

package proc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClockTicks returns the number of jiffies (clock ticks) per second.
// It first checks the env var CLK_TCK (useful for testing), otherwise
// falls back to 100 (common default).
//
// Note: On real systems, the authoritative way is `sysconf(_SC_CLK_TCK)`,
// but calling that requires cgo. For portability in a pure-Go library,
// this simplified approach is acceptable.
func ClockTicks() int {
	v, _ := strconv.Atoi(os.Getenv("CLK_TCK"))
	if v > 0 {
		return v
	}
	return 100
}

// PageSize returns the system memory page size in bytes.
// Like ClockTicks, it first checks an env override (PAGE_SIZE)
// to ease testing, then falls back to os.Getpagesize().
func PageSize() int {
	if ps := os.Getenv("PAGE_SIZE"); ps != "" {
		if v, _ := strconv.Atoi(ps); v > 0 {
			return v
		}
	}
	return os.Getpagesize()
}

// Exists reports whether a given PID currently exists in /proc.
// It simply checks if /proc/<pid> is a valid directory.
func Exists(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

//
// Per-PID readers
//

// ReadProcStat parses /proc/<pid>/stat and extracts four fields:
// - utime: user CPU jiffies
// - stime: system CPU jiffies
// - minflt: minor page faults (no I/O required)
// - majflt: major page faults (required I/O)
//
// Caveats:
//   - Field order is fixed, but comm (2nd field) is in parens and may contain
//     spaces. We strip everything before the closing ") " safely.
//   - Returns uint64 counters (monotonic increasing).
func ReadProcStat(pid int) (utime, stime, minflt, majflt uint64, err error) {
	f, e := os.Open(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return 0, 0, 0, 0, e
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, 0, 0, ErrNoStat
	}
	line := sc.Text()

	// Everything before ") " is pid + comm; after that are numeric fields.
	i := strings.LastIndex(line, ") ")
	if i < 0 {
		return 0, 0, 0, 0, ErrNoStat
	}
	fields := strings.Fields(line[i+2:])

	get := func(idx int) (uint64, error) {
		if idx >= len(fields) {
			return 0, ErrShortStat
		}
		return strconv.ParseUint(fields[idx], 10, 64)
	}

	// Indexes relative to fields slice:
	// minflt (8th overall) => fields[7]
	// majflt (10th overall) => fields[9]
	// utime (14th overall) => fields[11]
	// stime (15th overall) => fields[12]
	minflt, _ = get(7)
	majflt, _ = get(9)
	utime, _ = get(11)
	stime, _ = get(12)
	return
}

// ReadProcIO reads /proc/<pid>/io and returns read_bytes and write_bytes.
// These counters are monotonic and in bytes.
//
// Note: Not all processes expose this file (some kernel threads); in that case
// you’ll get an error.
func ReadProcIO(pid int) (readBytes, writeBytes uint64, err error) {
	f, e := os.Open(fmt.Sprintf("/proc/%d/io", pid))
	if e != nil {
		return 0, 0, e
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "read_bytes:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "read_bytes:"))
			readBytes, _ = strconv.ParseUint(v, 10, 64)
		} else if strings.HasPrefix(line, "write_bytes:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "write_bytes:"))
			writeBytes, _ = strconv.ParseUint(v, 10, 64)
		}
	}
	return readBytes, writeBytes, sc.Err()
}

// ReadProcRSS returns the Resident Set Size (RSS) in bytes for a PID.
// It prefers smaps_rollup (aggregated, since kernel 4.14) for accuracy.
// If unavailable, falls back to statm’s resident page count.
//
// Returns error if neither source is available.
func ReadProcRSS(pid int) (uint64, error) {
	// Prefer smaps_rollup
	if f, err := os.Open(fmt.Sprintf("/proc/%d/smaps_rollup", pid)); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "Rss:") {
				fs := strings.Fields(sc.Text())
				if len(fs) >= 2 {
					kb, _ := strconv.ParseUint(fs[1], 10, 64)
					return kb * 1024, nil
				}
			}
		}
	}
	// Fallback: statm field 2 × page size
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
		fs := strings.Fields(string(b))
		if len(fs) >= 2 {
			pages, _ := strconv.ParseUint(fs[1], 10, 64)
			return pages * uint64(PageSize()), nil
		}
	}
	return 0, ErrNoRSS
}

//
// System-level readers
//

// ReadSystemCPU parses /proc/stat for the aggregate CPU line and returns:
// - active: user + nice + system + irq + softirq + steal
// - total:  active + idle + iowait
//
// These are jiffy counters (monotonic increasing). You need to take
// deltas between samples to compute utilization.
func ReadSystemCPU() (active, total uint64, err error) {
	f, e := os.Open("/proc/stat")
	if e != nil {
		return 0, 0, e
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) == 0 || fs[0] != "cpu" {
			continue
		}
		if len(fs) < 8 {
			return 0, 0, ErrNoCPU
		}
		var vals []uint64
		for _, s := range fs[1:] {
			v, _ := strconv.ParseUint(s, 10, 64)
			vals = append(vals, v)
		}
		active = vals[0] + vals[1] + vals[2] + vals[5] + vals[6] + vals[7]
		total = active + vals[3] + vals[4]
		return active, total, nil
	}
	return 0, 0, ErrNoCPU
}

//
// Process tree
//

// ReadProcChildren returns the direct child PIDs of a process by reading
// /proc/<pid>/task/*/children files. Each children file lists space-separated
// PIDs for that thread’s children.
//
// Notes:
//   - Kernel 3.5+ exposes this interface.
//   - We deduplicate across threads by using a set.
//   - If no children are found, returns error.
func ReadProcChildren(pid int) ([]int, error) {
	glob := fmt.Sprintf("/proc/%d/task/*/children", pid)
	paths, _ := filepath.Glob(glob)
	set := map[int]struct{}{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, s := range strings.Fields(string(b)) {
			if id, err := strconv.Atoi(s); err == nil {
				set[id] = struct{}{}
			}
		}
	}
	out := make([]int, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, ErrNoChildren
	}
	return out, nil
}
