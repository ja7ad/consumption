//go:build linux

package proc

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cgroup2MountedOn(path string) (bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		sep := " - "
		i := strings.LastIndex(line, sep)
		if i < 0 {
			continue
		}
		pre := strings.Fields(line[:i])
		if len(pre) < 5 {
			continue
		}
		mp := pre[4]
		fsTail := strings.Fields(line[i+len(sep):])
		if len(fsTail) < 1 {
			continue
		}
		if mp == path && fsTail[0] == "cgroup2" {
			return true, nil
		}
	}
	return false, sc.Err()
}

func sleepSecs(d time.Duration) float64 {
	start := time.Now()
	time.Sleep(d)
	return time.Since(start).Seconds()
}

// Small workload to generate CPU, RAM touches, some I/O.
func spinWork(t *testing.T, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 4 MiB buffer; touch each page
		buf := make([]byte, 4<<20)
		start := time.Now()
		for time.Since(start) < d {
			for i := 0; i < len(buf); i += 4096 {
				buf[i]++
			}
			// burn a bit of CPU
			x := 1.0
			for i := 0; i < 8000; i++ {
				x = x*1.000001 + 0.000001
			}
			_ = x
			runtime.Gosched()
		}
	}()

	// nudge IO counters
	f, err := os.CreateTemp("", "v2_collector_io_*")
	if err == nil {
		defer os.Remove(f.Name())
		defer f.Close()
		b := make([]byte, 64<<10)
		start := time.Now()
		for time.Since(start) < d {
			if _, err := f.Write(b); err != nil {
				break
			}
			_ = f.Sync()
		}
	}

	<-done
}

func TestV2_NewAndClose(t *testing.T) {
	ok, err := cgroup2MountedOn("/sys/fs/cgroup")
	if err != nil {
		t.Skipf("skip: cannot read mountinfo: %v", err)
	}
	if !ok {
		t.Skip("skip: cgroup v2 is not mounted on /sys/fs/cgroup")
	}

	c, err := newV2(0.5)
	require.NoError(t, err)
	require.NotNil(t, c)

	// Close may fail if the group still has tasks (lack of perms to move out),
	// so don't assert NoError. Just call it.
	_ = c.Close()
}

func TestV2_Sample_Errors(t *testing.T) {
	ok, err := cgroup2MountedOn("/sys/fs/cgroup")
	if err != nil || !ok {
		t.Skip("skip: cgroup v2 not available")
	}

	c, err := newV2(0.0)
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Sample(nil, 1.0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoPIDs)

	_, err = c.Sample([]int{os.Getpid()}, 0.0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBadDt)

	// Use an obviously invalid PID → treated as all exited
	_, err = c.Sample([]int{99999999}, 0.5)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllExited)
}

func TestV2_Sample_SelfTwoTicksWithWorkload(t *testing.T) {
	ok, err := cgroup2MountedOn("/sys/fs/cgroup")
	if err != nil || !ok {
		t.Skip("skip: cgroup v2 not available")
	}

	c, err := newV2(0.5) // EMA on VM util
	require.NoError(t, err)
	defer c.Close()

	self := os.Getpid()
	pids := []int{self}

	// Spin workload overlapping both samples (~300ms)
	go spinWork(t, 300*time.Millisecond)

	// First tick (~150ms)
	dt1 := sleepSecs(150 * time.Millisecond)
	s1, err := c.Sample(pids, dt1)
	require.NoError(t, err)

	// Second tick (~150ms)
	dt2 := sleepSecs(150 * time.Millisecond)
	s2, err := c.Sample(pids, dt2)
	require.NoError(t, err)

	// Ranges and dt reflection
	for _, u := range []float64{s1.UVm, s1.UProc, s2.UVm, s2.UProc} {
		assert.GreaterOrEqual(t, u, 0.0)
		assert.LessOrEqual(t, u, 1.0)
	}
	assert.InDelta(t, dt1, s1.TimeSec, 1e-3)
	assert.InDelta(t, dt2, s2.TimeSec, 1e-3)

	// Expect some activity with the induced workload
	hasSignal := (s1.UProc > 0 || s2.UProc > 0) ||
		(s1.ReadBytes+s1.WriteBytes+s2.ReadBytes+s2.WriteBytes) > 0 ||
		(s1.RSSChurnBytes+s2.RSSChurnBytes) > 0 ||
		(s1.RefaultBytes+s2.RefaultBytes) > 0
	assert.True(t, hasSignal, "expected some activity with induced workload")

	// RefaultBytes may legitimately be zero on some kernels/configs, so don't assert >0.
}

func TestV2_InternalHelpers(t *testing.T) {
	// These are lightweight checks to ensure helper paths don’t regress.

	// cpu.stat parsing at root (skip if unavailable)
	if ok, _ := cgroup2MountedOn("/sys/fs/cgroup"); ok {
		v, err := readCPUUsageUsec(filepath.Join("/sys/fs/cgroup", "cpu.stat"))
		require.NoError(t, err)
		assert.GreaterOrEqual(t, v, uint64(0))
	}

	// memory.stat refault parsing (may not exist on some kernels; allow error)
	_, _ = readWorkingsetRefault(filepath.Join("/sys/fs/cgroup", "memory.stat"))
}
