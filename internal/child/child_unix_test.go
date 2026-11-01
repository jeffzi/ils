//go:build unix

package child_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/jeffzi/ils/internal/child"
)

func TestProgram_Run_WhenAScriptsInterpreterIsMissingAndAStartWantingAFileIsMissing_ReturnsToolMissingError(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "gh")
	assert.NilError(t, os.WriteFile(script, []byte("#!/no/such/interpreter\n"), 0o700)) //nolint:gosec // the script must be executable for its start to reach the interpreter
	program := child.Program{Tool: "gh", Args: []string{"pr", "view"}, LookPath: foundAt(script), StartWantingAFileIsMissing: true}

	_, err := program.Run(t.Context())

	_, missing := errors.AsType[*child.ToolMissingError](err)
	assert.Check(t, missing, "Run error = %v, want *child.ToolMissingError", err)
}
