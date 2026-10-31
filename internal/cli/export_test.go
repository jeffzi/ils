package cli

import (
	"runtime/debug"

	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/git"
)

// SetPush makes a run `ils push` with run in place of landing.Push.
func (a *App) SetPush(run Command) {
	a.runPush = run
}

// SetMerge makes a run `ils merge` with run in place of landing.Merge.
func (a *App) SetMerge(run Command) {
	a.runMerge = run
}

// SetBranch makes a run `ils branch <name>` with run in place of landing.StartBranch.
func (a *App) SetBranch(run Command) {
	a.runBranch = run
}

// SetRelease makes a run `ils release <version>` with run in place of landing.Release.
func (a *App) SetRelease(run Command) {
	a.runRelease = run
}

// SetGit makes a run call git through runner in place of git in the working directory.
func (a *App) SetGit(runner git.Runner) {
	a.git = runner
}

// SetBuildInfo makes a run read info and ok as its build info in place of debug.ReadBuildInfo.
func (a *App) SetBuildInfo(info *debug.BuildInfo, ok bool) {
	a.buildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
}

// FrameEvery is frameEvery, for the tests of package cli_test.
const FrameEvery = frameEvery

// PendingHint is pendingHint, for the tests of package cli_test.
const PendingHint = pendingHint

// RootSwitches returns the flags the root command of ils knows before it finds a command and that
// take no value, each spelled as typed: `--name` and, when it has one, `-x`.
func (a *App) RootSwitches() []string {
	root := (&parse{app: *a}).root()

	var switches []string
	visit := func(f *pflag.Flag) {
		if f.NoOptDefVal == "" {
			return
		}

		switches = append(switches, "--"+f.Name)
		if f.Shorthand != "" {
			switches = append(switches, "-"+f.Shorthand)
		}
	}
	root.Flags().VisitAll(visit)
	root.PersistentFlags().VisitAll(visit)

	return switches
}
