//go:build linux

package proc

import (
	"fmt"

	"github.com/ja7ad/consumption/pkg/system/cgroup"
	"github.com/ja7ad/consumption/pkg/types"
)

type Snapshot struct {
	TimeSec float64
	// Utilizations in [0,1]
	UVm   float64
	UProc float64
	// Byte deltas for this window
	ReadBytes  types.Bytes
	WriteBytes types.Bytes
	// RAM proxies (bytes)
	RefaultBytes  types.Bytes // v2 only (memory.stat workingset_refault * pagesize)
	RSSChurnBytes types.Bytes
}

type Collector interface {
	Sample(pids []int, dtSec float64) (Snapshot, error)
	Close() error
}

// NewCollector returns a Collector implementation chosen by the detected cgroup mode.
// - V2 or Hybrid: prefer v2 (more accurate CPU attribution).
// - V1: fallback to /proc-only collector.
func NewCollector(alpha float64) (Collector, error) {
	ver, _, err := cgroup.Detect()
	if err != nil {
		return nil, fmt.Errorf("collector: detect cgroup: %w", err)
	}

	switch ver {
	case cgroup.V2:
		return newV2(alpha)
	case cgroup.Hybrid:
		return newV2(alpha)
	case cgroup.V1:
		return newV1(alpha)
	default:
		return nil, ErrUnsupported
	}
}
