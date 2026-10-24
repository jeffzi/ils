//go:build unix

package main_test

import (
	"testing"

	"gotest.tools/v3/assert"

	main "github.com/jeffzi/ils"
)

func TestForeground_WhenTheDescriptorIsNoTerminal_CountsAsTheForegroundJob(t *testing.T) {
	t.Parallel()

	file := opened(t, "stderr")

	assert.Check(t, main.Foreground(file.Fd()))
}
