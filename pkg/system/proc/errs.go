package proc

import "errors"

var (
	// ErrNoStat indicates that /proc/<pid>/stat was empty or malformed.
	ErrNoStat = errors.New("proc: malformed or empty stat")

	// ErrNoRSS indicates that resident set size could not be determined
	// (neither smaps_rollup nor statm succeeded).
	ErrNoRSS = errors.New("proc: no rss")

	// ErrNoCPU indicates that /proc/stat had no aggregate CPU line.
	ErrNoCPU = errors.New("proc: no cpu line")

	// ErrNoChildren indicates that /proc/<pid>/task/*/children contained none.
	ErrNoChildren = errors.New("proc: no children")

	// ErrShortStat indicates that /proc/<pid>/stat had fewer fields than expected.
	ErrShortStat = errors.New("proc: short stat")

	// ErrNoPIDs means caller passed an empty slice.
	ErrNoPIDs = errors.New("collector: no pids")

	// ErrAllExited means none of the requested PIDs existed at sampling time.
	ErrAllExited = errors.New("collector: all pids exited")

	// ErrBadDt means dtSec <= 0 was provided to Sample.
	ErrBadDt = errors.New("collector: dtSec must be > 0")

	// ErrUnsupported collector fails because the detected cgroup mode is unsupported.
	ErrUnsupported = errors.New("collector: unsupported cgroup mode")
)
