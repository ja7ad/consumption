//go:build linux

package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Version int

const (
	Unsupported Version = iota // non-Linux or no cgroup mounts
	V1                         // legacy multi-hierarchy cgroup v1
	V2                         // unified cgroup v2
	Hybrid                     // both v1 and v2 present
)

func (v Version) String() string {
	switch v {
	case V1:
		return "cgroup v1"
	case V2:
		return "cgroup v2"
	case Hybrid:
		return "cgroup hybrid"
	default:
		return "unsupported"
	}
}

// Detect returns the detected cgroup version and a human-readable detail string.
//
// It parses /proc/self/mountinfo looking for cgroup filesystems.
// The line format has a " - fstype " separator; we only care about fstype.
func Detect() (Version, string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return Unsupported, "", fmt.Errorf("open mountinfo: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var (
		hasV1 bool
		hasV2 bool
		v1Pts []string
		v2Pts []string
		sc    = bufio.NewScanner(f)
	)
	for sc.Scan() {
		line := sc.Text()
		// mountinfo has: <fields> - <fstype> <source> <superopts>
		sep := " - "
		i := strings.LastIndex(line, sep)
		if i < 0 {
			continue
		}
		tail := line[i+len(sep):]
		fields := strings.Fields(tail)
		if len(fields) < 1 {
			continue
		}
		fstype := fields[0]

		// Extract the mount point (field 5 in the pre-separator part)
		// Ref: man 5 proc
		pre := strings.Fields(line[:i])
		if len(pre) < 5 {
			continue
		}
		mountPoint := pre[4]

		switch fstype {
		case "cgroup2":
			hasV2 = true
			v2Pts = append(v2Pts, mountPoint)
		case "cgroup":
			hasV1 = true
			v1Pts = append(v1Pts, mountPoint)
		}
	}
	if err := sc.Err(); err != nil {
		return Unsupported, "", fmt.Errorf("scan mountinfo: %w", err)
	}

	switch {
	case hasV1 && hasV2:
		return Hybrid, fmt.Sprintf("cgroup2 on %v; cgroup v1 on %v",
			strings.Join(v2Pts, ","), strings.Join(v1Pts, ",")), nil
	case hasV2:
		return V2, fmt.Sprintf("cgroup2 on %v", strings.Join(v2Pts, ",")), nil
	case hasV1:
		return V1, fmt.Sprintf("cgroup v1 on %v", strings.Join(v1Pts, ",")), nil
	default:
		return Unsupported, "no cgroup mounts found", nil
	}
}

// MustDetect is a convenience that panics on error.
func MustDetect() Version {
	v, _, err := Detect()
	if err != nil {
		panic(err)
	}
	return v
}
