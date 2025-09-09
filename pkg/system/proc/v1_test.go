//go:build linux

package proc

import (
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper: sleep and return precise dt in seconds
func sleepSec(d time.Duration) float64 {
	start := time.Now()
	time.Sleep(d)
	return time.Since(start).Seconds()
}

func TestV1_NewAndClose(t *testing.T) {
	c, err := newV1(0.5) // EMA enabled
	require.NoError(t, err)
	require.NotNil(t, c)
	require.NoError(t, c.Close())
}

func TestV1_Sample_Errors(t *testing.T) {
	c, err := newV1(0.0)
	require.NoError(t, err)

	// empty pid slice
	_, err = c.Sample(nil, 1.0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoPIDs))

	// dtSec <= 0
	_, err = c.Sample([]int{os.Getpid()}, 0.0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBadDt))

	// all pids exited (use a very large PID)
	_, err = c.Sample([]int{99999999}, 1.0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAllExited))
}

func TestV1_Sample_SelfSingleTick(t *testing.T) {
	c, err := newV1(0.0) // no EMA to keep raw behavior
	require.NoError(t, err)
	defer c.Close()

	pids := []int{os.Getpid()}

	// Let some CPU jiffies elapse from constructor's baseline
	dt := sleepSec(100 * time.Millisecond)

	snap, err := c.Sample(pids, dt)
	require.NoError(t, err)

	// Basic invariants
	assert.InDelta(t, dt, snap.TimeSec, 1e-3, "TimeSec should echo dtSec")
	assert.GreaterOrEqual(t, snap.UVm, 0.0)
	assert.LessOrEqual(t, snap.UVm, 1.0)
	assert.GreaterOrEqual(t, snap.UProc, 0.0)
	assert.LessOrEqual(t, snap.UProc, 1.0)

	// Byte deltas are >= 0
	assert.GreaterOrEqual(t, snap.ReadBytes, uint64(0))
	assert.GreaterOrEqual(t, snap.WriteBytes, uint64(0))
	assert.GreaterOrEqual(t, snap.RefaultBytes, uint64(0))
	assert.GreaterOrEqual(t, snap.RSSChurnBytes, uint64(0))
}

func TestV1_Sample_TwoTicksAndUtilRanges(t *testing.T) {
	// Enable EMA to exercise smoothing path too
	c, err := newV1(0.5)
	require.NoError(t, err)
	defer c.Close()

	pids := []int{os.Getpid()}

	// Kick off workload overlapping both samples (~300ms)
	go doWork(t, 300*time.Millisecond)

	// First tick (≈150ms)
	dt1 := sleepSec(150 * time.Millisecond)
	s1, err := c.Sample(pids, dt1)
	require.NoError(t, err)

	// Second tick (≈150ms)
	dt2 := sleepSec(150 * time.Millisecond)
	s2, err := c.Sample(pids, dt2)
	require.NoError(t, err)

	// Ranges [0,1]
	for _, u := range []float64{s1.UVm, s1.UProc, s2.UVm, s2.UProc} {
		assert.GreaterOrEqual(t, u, 0.0)
		assert.LessOrEqual(t, u, 1.0)
	}

	// TimeSec ≈ dt
	assert.InDelta(t, dt1, s1.TimeSec, 1e-3)
	assert.InDelta(t, dt2, s2.TimeSec, 1e-3)

	// With the induced workload we should see some signal now
	hasSignal := (s1.UProc > 0) || (s2.UProc > 0) ||
		(s1.ReadBytes+s1.WriteBytes+s2.ReadBytes+s2.WriteBytes) > 0 ||
		(s1.RSSChurnBytes+s2.RSSChurnBytes) > 0 ||
		(s1.RefaultBytes+s2.RefaultBytes) > 0
	assert.True(t, hasSignal, "expected some activity with induced workload")
}

func TestV1_Sample_HandlesPIDExitBetweenTicks(t *testing.T) {
	c, err := newV1(0.0)
	require.NoError(t, err)
	defer c.Close()

	// Spawn a short-lived child process: /bin/sleep 0.1
	p, err := os.StartProcess("/bin/sleep", []string{"sleep", "0.1"}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		t.Skipf("skip: cannot start /bin/sleep: %v", err)
		return
	}
	pid := p.Pid

	// First sample with child alive
	dt1 := sleepSec(50 * time.Millisecond)
	_, err = c.Sample([]int{pid}, dt1)
	// It's possible the process has already exited (fast), so allow either outcome:
	if err != nil {
		assert.True(t, errors.Is(err, ErrAllExited))
	}

	// Ensure process is gone
	_, err = p.Wait()
	require.NoError(t, err)

	// Second sample should now report ErrAllExited
	dt2 := sleepSec(50 * time.Millisecond)
	_, err = c.Sample([]int{pid}, dt2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAllExited))
}

func doWork(t *testing.T, d time.Duration) {
	t.Helper()
	// CPU + RAM
	done := make(chan struct{})
	go func() {
		// allocate ~4MB and touch it in a loop
		buf := make([]byte, 4<<20)
		start := time.Now()
		for time.Since(start) < d {
			for i := 0; i < len(buf); i += 4096 {
				buf[i]++
			}
			// a little CPU math
			x := 1.0
			for i := 0; i < 1_0000; i++ {
				x = x*1.000001 + 0.000001
			}
			_ = x
			runtime.Gosched()
		}
		close(done)
	}()

	// Disk I/O: write a small temp file repeatedly during the window
	f, err := os.CreateTemp("", "v1_collector_io_*")
	if err == nil {
		defer os.Remove(f.Name())
		defer f.Close()
		buf := make([]byte, 64<<10) // 64 KiB
		start := time.Now()
		for time.Since(start) < d {
			if _, err := f.Write(buf); err != nil {
				break
			}
			// flush to nudge kernel accounting
			_ = f.Sync()
		}
	}

	<-done
}
