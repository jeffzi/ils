package quiet_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/quiet"
)

func TestSetting(t *testing.T) {
	// Not parallel: each row sets GORACE with t.Setenv.
	tests := []struct {
		name   string
		gorace string
		want   string
	}{
		{name: "no options in the environment", gorace: "", want: "atexit_sleep_ms=0"},
		{name: "options in the environment", gorace: "halt_on_error=1", want: "halt_on_error=1 atexit_sleep_ms=0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GORACE", tt.gorace)

			got := quiet.Setting()

			assert.Check(t, is.Equal(got, tt.want))
		})
	}
}
