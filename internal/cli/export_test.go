package cli

import (
	"runtime/debug"

	"github.com/jeffzi/ils/internal/git"
)

// SetPush makes a run `ils push` with run in place of landing.Push.
func (a *App) SetPush(run Command) {
	a.runPush = run
}

// SetGit makes a run call git through runner in place of git in the working directory.
func (a *App) SetGit(runner git.Runner) {
	a.git = runner
}

// SetBuildInfo makes a run read info and ok as its build info in place of debug.ReadBuildInfo.
func (a *App) SetBuildInfo(info *debug.BuildInfo, ok bool) {
	a.buildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
}
