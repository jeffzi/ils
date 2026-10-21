package main_test

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/gittest"
)

// ran is what one run of the binary printed and the code it exited with.
type ran struct {
	stdout string
	stderr string
	code   int
}

// coverDir returns the directory `go test -cover` collects coverage data in, or "" when the test
// binary runs without coverage.
func coverDir() string {
	f := flag.Lookup("test.gocoverdir")
	if f == nil {
		return ""
	}

	return f.Value.String()
}

// binary is the path of an ils binary built from the module root.
type binary string

// build compiles the module root into a binary under the test's temporary directory.
//
// Under coverage the binary is instrumented with this test's counter mode and for this package
// alone: `go test` merges into its profile only the coverage data whose meta-data matches its own,
// and any other mode or package set records every line of main.go as never run.
func build(t *testing.T) binary {
	t.Helper()

	name := "ils"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	bin := filepath.Join(t.TempDir(), name)

	args := []string{"build", "-o", bin}
	if coverDir() != "" {
		args = append(args, "-cover", "-covermode="+testing.CoverMode(), "-coverpkg="+coveredPackages(t))
	}

	out, err := exec.CommandContext(t.Context(), "go", append(args, ".")...).CombinedOutput() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return binary(bin)
}

// coveredPackages returns, comma-separated, the import paths of the packages this test binary is
// instrumented for, which `go test` picks from its whole package pattern. It runs the test binary
// again with no test selected, so that the binary writes its coverage meta-data and nothing else.
func coveredPackages(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	out, err := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$", "-test.gocoverdir="+dir).CombinedOutput() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("write the coverage meta-data of the test binary: %v\n%s", err, out)
	}

	out, err = exec.CommandContext(t.Context(), "go", "tool", "covdata", "pkglist", "-i="+dir).Output() //nolint:gosec // argv is written by this function
	if err != nil {
		t.Fatalf("list the packages covered by the test binary: %v", err)
	}

	return strings.Join(strings.Fields(string(out)), ",")
}

// launch runs the binary with args in dir under env, plus the coverage directory when there is one,
// and returns what it printed.
func (b binary) launch(t *testing.T, dir string, env []string, args ...string) ran {
	t.Helper()

	if covered := coverDir(); covered != "" {
		env = append(env, "GOCOVERDIR="+covered)
	}

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), string(b), args...) //nolint:gosec // argv is written by the tests in this file
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", b, err)
	}

	return ran{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func TestIls(t *testing.T) {
	t.Parallel()

	bin := build(t)

	// The two runs below go one after the other: two instrumented binaries that exit at once race
	// to write the same coverage meta-data file, and the loser prints the failure on its stderr.
	t.Run("WhenAskedForTheVersion_PrintsTheModuleVersionOfTheBinary", func(t *testing.T) {
		info, err := buildinfo.ReadFile(string(bin))
		assert.NilError(t, err)

		stampedFromCheckout := slices.ContainsFunc(info.Settings, func(s debug.BuildSetting) bool {
			return s.Key == "vcs"
		})
		if !stampedFromCheckout {
			t.Skip("the binary was built from a source tree with no version control, so its version is (devel)")
		}

		want := cli.Version(info, true)
		assert.Assert(t, want != "(devel)", "the build info of %s names no module version", bin)

		got := bin.launch(t, t.TempDir(), os.Environ(), "--version")

		assert.Check(t, is.Equal(got.code, 0))
		assert.Check(t, is.Equal(got.stdout, "ils version "+want+"\n"))
		assert.Check(t, is.Equal(got.stderr, ""))
	})

	t.Run("WhenPushRunsOutsideARepository_ReportsTheFailedReadOfHead", func(t *testing.T) {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		assert.NilError(t, err)

		env := append(gittest.Env(t), "GIT_CEILING_DIRECTORIES="+dir, "LC_ALL=C")

		got := bin.launch(t, dir, env, "push")

		assert.Check(t, is.Equal(got.code, 1))
		assert.Check(t, is.Equal(got.stderr, ""))
		assert.Check(t, strings.HasPrefix(got.stdout, "Could not read where HEAD stands: "),
			"stdout = %q", got.stdout)
		assert.Check(t, strings.HasSuffix(got.stdout, "Nothing was written.\nNext: fix what failed, then run ils push again\n"),
			"stdout = %q", got.stdout)
	})
}
