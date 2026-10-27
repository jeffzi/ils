package cli

import (
	"runtime/debug"

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

// SetStatus makes a run `ils status` with run in place of landing.Status.
func (a *App) SetStatus(run Command) {
	a.runStatus = run
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

// Bound is bound, for the tests of package cli_test.
const Bound = bound

// PendingHint is pendingHint, for the tests of package cli_test.
const PendingHint = pendingHint
