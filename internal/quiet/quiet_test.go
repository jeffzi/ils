package quiet_test

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/quiet"
)

func TestSetting_WhenEnvironmentHoldsNoOptions_StartsWithNoSpace(t *testing.T) {
	// Not parallel: this sets GORACE with t.Setenv.
	t.Setenv("GORACE", "")

	got := quiet.Setting()

	assert.Check(t, !strings.HasPrefix(got, " "), "Setting() = %q, want no leading space", got)
}

func TestSetting_WhenEnvironmentHoldsOptions_KeepsThemFirst(t *testing.T) {
	// Not parallel: this sets GORACE with t.Setenv.
	t.Setenv("GORACE", "halt_on_error=1")

	got := quiet.Setting()

	assert.Check(t, strings.HasPrefix(got, "halt_on_error=1 "), "Setting() = %q, want the environment's options first", got)
}
