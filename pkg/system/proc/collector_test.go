//go:build linux

package proc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollector(t *testing.T) {
	col, err := NewCollector(0.0)
	require.NoError(t, err)
	require.NotNil(t, col)
}
