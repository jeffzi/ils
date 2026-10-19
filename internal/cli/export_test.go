package cli

import "github.com/jeffzi/ils/internal/git"

// SetPush makes a run `ils push` with run in place of landing.Push.
func (a *App) SetPush(run Command) {
	a.runPush = run
}

// SetGit makes a run call git through runner in place of git in the working directory.
func (a *App) SetGit(runner git.Runner) {
	a.git = runner
}
