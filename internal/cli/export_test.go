package cli

import (
	"runtime/debug"
	"slices"

	"github.com/spf13/cobra"

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

// Root returns the root command of ils, for the tests of package cli_test.
func (a *App) Root() *cobra.Command {
	return (&parse{app: *a}).root()
}

// SetGuardSupported makes `ils init claude` act as on a system where the guard runs when supported
// is true, and as on one where the guard refuses every run otherwise.
func (a *App) SetGuardSupported(supported bool) {
	a.guardRuns = &supported
}

// GuardParsesFlags is guardSupported, for the tests of package cli_test: `ils guard` and
// `ils parse` parse their flags only where the guard runs.
const GuardParsesFlags = guardSupported

// UserHook is userHook, for the tests of package cli_test.
const UserHook = userHook

// ProjectHook is projectHook, for the tests of package cli_test.
const ProjectHook = projectHook

// HookTimeout is hookTimeout as the number a decoded settings file holds, for the tests of package
// cli_test.
const HookTimeout float64 = hookTimeout

// HookOnFailure is hookOnFailure, for the tests of package cli_test.
const HookOnFailure = hookOnFailure

// PermissionRules returns the rules `ils init claude` adds to each permission list, by the name of
// the list, for the tests of package cli_test.
func PermissionRules() map[string][]string {
	rules := map[string][]string{}
	for _, list := range permissionLists {
		rules[list.name] = slices.Clone(list.rules)
	}

	return rules
}
