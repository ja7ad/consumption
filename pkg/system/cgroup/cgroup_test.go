//go:build linux

package cgroup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Detect(t *testing.T) {
	ver, str, err := Detect()
	require.NoError(t, err)

	assert.NotEmpty(t, str)
	assert.NotEqual(t, ver, Unsupported)

	t.Logf("detected %s: %s", ver, str)
}

func Test_MustDetect(t *testing.T) {
	ver := MustDetect()
	assert.NotEqual(t, ver, Unsupported)

	t.Logf("detected %s", ver)
}
