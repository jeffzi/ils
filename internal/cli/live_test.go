//go:build live

package cli_test

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
	"github.com/jeffzi/ils/internal/testtarget"
)

// The live suite runs `ils push` and `ils merge` with the real git and the real gh against a test
// repository on GitHub. `task test:live` runs it, and `task test:setup-live` creates the repositories it needs.

const (
	// liveGhTargetVar says which gh the live suite must find: testtarget.Minimum, testtarget.Latest,
	// or unset for latest.
	liveGhTargetVar = "ILS_LIVE_GH"
	// liveGhVersionVar, when set, is the exact version of gh the live suite must find.
	liveGhVersionVar = "ILS_LIVE_GH_VERSION"
	// liveGitTargetVar says which git the live suite must find: testtarget.Minimum,
	// testtarget.Latest, or unset for latest.
	liveGitTargetVar = "ILS_LIVE_GIT"
	// liveGitVersionVar, when set, is the exact version of git the live suite must find.
	liveGitVersionVar = "ILS_LIVE_GIT_VERSION"
)

func liveGitTool() testtarget.Tool {
	return testtarget.Tool{
		Name: "git", TargetVar: liveGitTargetVar, VersionVar: liveGitVersionVar, MinimumVersion: git.MinimumVersion,
	}
}

func liveGhTool() testtarget.Tool {
	return testtarget.Tool{
		Name: "gh", TargetVar: liveGhTargetVar, VersionVar: liveGhVersionVar, MinimumVersion: github.MinimumVersion,
	}
}

// checkLiveTool returns why out, the output of `<tool> --version`, does not suit the live suite
// given the target and version the environment sets for tool, or nil.
func checkLiveTool(tool testtarget.Tool, out string, versionOf func(string) string) error {
	return tool.CheckOutput(os.Getenv(tool.TargetVar), os.Getenv(tool.VersionVar), out, versionOf)
}

const (
	// liveOriginVar holds the clone URL of the test repository the suite resets and writes to.
	liveOriginVar = "ILS_LIVE_ORIGIN"

	// liveNamePrefix starts the name of every repository the suite agrees to reset.
	liveNamePrefix = "ils-live-"

	// liveScenarioLimit is how long one scenario may run `ils push` or `ils merge` again while it is
	// pending.
	liveScenarioLimit = 8 * time.Minute
	liveCleanupLimit  = time.Minute
	liveRegistryPoll  = 5 * time.Second

	liveListLimit = "1000"
	// liveTokenLength is how many characters of random text tell one run's branch and commits from
	// another's.
	liveTokenLength = 10

	liveWorkflowDir  = ".github/workflows/"
	liveWorkflowPath = liveWorkflowDir + "live.yml"
	// liveDispatchedPath is the workflow the trunk carries in the scenario whose CI does not fire.
	liveDispatchedPath = liveWorkflowDir + "dispatched.yml"
	// liveNote is the file every commit of the suite writes, so that no two runs share a commit.
	liveNote = "live.txt"
	// liveContested is the file the conflict scenario changes on the branch and on the trunk.
	liveContested = "contested.txt"
	// liveJob is the one job of every workflow the suite pushes, and so the name of its check. No
	// wording of ils holds it, so finding it in a result means the result named the check.
	liveJob = "probe"
	// livePassing is the step of a workflow whose check passes.
	livePassing = "echo passed"
)

// liveSuite is the test repository the suite writes to.
type liveSuite struct {
	// url is the clone URL liveOriginVar holds, and origin the repository it names.
	url    string
	origin git.Origin
	trunk  string
}

// liveBranch is one scenario's fresh clone, on a branch of its own.
type liveBranch struct {
	suite *liveSuite
	// deadline is when the scenario stops running `ils push` or `ils merge` again.
	deadline time.Time
	dir      string
	name     string
	// token is in no earlier run's branch or commit.
	token string
}

// liveRun is one result of `ils push --json` or `ils merge --json`.
type liveRun struct {
	// raw is the JSON as printed, for failure messages.
	raw   string
	shown resulttest.Shown
	code  int
}

// livePull is an open pull request of a scenario's branch, as GitHub shows it.
type livePull struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Base   string `json:"baseRefName"`
	Head   string `json:"headRefOid"`
	Number int    `json:"number"`
	Draft  bool   `json:"isDraft"`
}

// ---------------------------------------------------------------------------
// Command runners
// ---------------------------------------------------------------------------

// liveGh runs gh with args and returns its trimmed stdout. Every call that names a repository
// names the suite's own with --repo or in the path, so that neither GH_REPO nor the directory
// chooses one.
func liveGh(ctx context.Context, args ...string) (string, error) {
	got, err := github.Runner{}.Run(ctx, args...)

	return strings.TrimSpace(got.Stdout), err
}

// liveGit runs git in dir with the caller's own configuration and returns its trimmed stdout.
func liveGit(ctx context.Context, dir string, args ...string) (string, error) {
	got, err := git.Runner{Dir: dir}.Run(ctx, args...)

	return strings.TrimSpace(got.Stdout), err
}

// mustGh is liveGh that stops the test when gh fails.
func mustGh(t *testing.T, args ...string) string {
	t.Helper()

	out, err := liveGh(t.Context(), args...)
	assert.NilError(t, err)

	return out
}

// mustGit is liveGit that stops the test when git fails.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := liveGit(t.Context(), dir, args...)
	assert.NilError(t, err)

	return out
}

// ---------------------------------------------------------------------------
// Test repository and reset
// ---------------------------------------------------------------------------

// newLiveSuite returns the repository liveOriginVar names, reset to its first commit, once the
// name, the gh and git found, and GitHub's refusal of the rules read are what the suite needs.
// Nothing is written before the name, gh, and git are checked.
func newLiveSuite(t *testing.T) *liveSuite {
	t.Helper()

	l := &liveSuite{url: os.Getenv(liveOriginVar)}
	assert.Assert(t, l.url != "", "%s is unset or empty, want the clone URL of an %s* repository", liveOriginVar, liveNamePrefix)

	origin, err := git.ParseOrigin(l.url)
	assert.NilError(t, err, "%s", liveOriginVar)
	assert.Assert(t, strings.HasPrefix(origin.Name, liveNamePrefix) && origin.Name != liveNamePrefix,
		"%s names the repository %s; the suite resets only a repository named %s<something>",
		liveOriginVar, origin.Name, liveNamePrefix)

	l.origin = origin

	assert.NilError(t, checkLiveTool(liveGhTool(), mustGh(t, "--version"), github.VersionOf))
	assert.NilError(t, checkLiveTool(liveGitTool(), mustGit(t, "", "--version"), git.VersionOf))
	l.reset(t)
	l.checkRulesRead(t)

	return l
}

// repo is the repository as owner/name.
func (l *liveSuite) repo() string {
	return l.origin.Owner + "/" + l.origin.Name
}

// slug is the repository as gh's --repo flag names it.
func (l *liveSuite) slug() string {
	return l.origin.Host + "/" + l.repo()
}

// apiPath is the path of gh api for rest, a path under the repository.
func (l *liveSuite) apiPath(rest string) string {
	return "repos/" + l.repo() + "/" + rest
}

// apiArgs is the argument list of gh api for rest, a path under the repository, followed by extra.
func (l *liveSuite) apiArgs(rest string, extra ...string) []string {
	return append([]string{"api", "--hostname", l.origin.Host, "--paginate", l.apiPath(rest)}, extra...)
}

// remoteCommit is the commit ref stands on in the repository on GitHub, and empty when the ref is
// missing.
func (l *liveSuite) remoteCommit(t *testing.T, ref string) string {
	t.Helper()

	sha, _, _ := strings.Cut(mustGit(t, "", "ls-remote", l.url, ref), "\t")

	return sha
}

// mustRemoteCommit is remoteCommit for a ref that must exist: it fails the test when the ref is
// missing.
func (l *liveSuite) mustRemoteCommit(t *testing.T, ref string) string {
	t.Helper()

	sha := l.remoteCommit(t, ref)
	if sha == "" {
		t.Fatalf("%s has no commit on %s", l.url, ref)
	}

	return sha
}

func (l *liveSuite) trunkRef() string {
	return headRef(l.trunk)
}

func headRef(name string) string { return "refs/heads/" + name }

// liveToken returns random text in no earlier run's branch or commit.
func liveToken() string { return strings.ToLower(rand.Text())[:liveTokenLength] }

// liveBranchName returns the name of the branch of scenario in the run token tells.
func liveBranchName(scenario, token string) string { return "live/" + scenario + "-" + token }

// clone returns a fresh clone of the repository that commits under a name of its own and
// signs nothing.
func (l *liveSuite) clone(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "clone")
	mustGit(t, "", "clone", "--quiet", l.url, dir)
	mustGit(t, dir, "config", "user.name", "ils live suite")
	mustGit(t, dir, "config", "user.email", "ils-live@example.com")
	mustGit(t, dir, "config", "commit.gpgsign", "false")

	return dir
}

// reset puts the repository back to a known state, whatever an earlier run left: the default
// branch on its first commit, no open pull request, no other branch, and no run in progress.
func (l *liveSuite) reset(t *testing.T) {
	t.Helper()

	dir := l.clone(t)
	l.trunk = mustGh(t, "repo", "view", l.slug(), "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	root := mustGit(t, dir, "rev-list", "--max-parents=0", "origin/"+l.trunk)
	if workflows := mustGit(t, dir, "ls-tree", "--name-only", root, liveWorkflowDir); workflows != "" {
		t.Fatalf("the first commit of %s carries %s, so the scenario with no CI has CI to find", l.trunk, workflows)
	}
	mustGit(t, dir, "push", "--quiet", "--force", l.url, root+":"+l.trunkRef())

	open := mustGh(t, "pr", "list", "--repo", l.slug(), "--state", "open", "--limit", liveListLimit,
		"--json", "number", "--jq", ".[].number")
	for number := range strings.FieldsSeq(open) {
		mustGh(t, "pr", "close", number, "--repo", l.slug())
	}

	var others []string

	for line := range strings.Lines(mustGit(t, dir, "ls-remote", "--heads", l.url)) {
		_, ref, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if ref != "" && ref != l.trunkRef() {
			others = append(others, ":"+ref)
		}
	}

	if len(others) > 0 {
		mustGit(t, dir, append([]string{"push", "--quiet", l.url}, others...)...)
	}

	assert.NilError(t, l.cancelRuns(t.Context()))
}

// cancelRuns cancels every workflow run of the repository that has not completed. A run that
// completed before its cancel arrived is no failure.
func (l *liveSuite) cancelRuns(ctx context.Context) error {
	var failed []error

	// One list per status GitHub gives a run that has not completed. The REST API takes each of
	// them as a filter on every gh version, where `gh run list --status` rejects `pending` on the
	// minimum gh.
	for _, status := range []string{"requested", "waiting", "pending", "queued", "in_progress"} {
		running, err := liveGh(ctx, l.apiArgs("actions/runs?per_page=100&status="+status,
			"--jq", ".workflow_runs[].id")...)
		if err != nil {
			return fmt.Errorf("list %s runs of %s: %w", status, l.slug(), err)
		}

		for id := range strings.FieldsSeq(running) {
			if _, err := liveGh(ctx, "run", "cancel", id, "--repo", l.slug()); err != nil && !l.completed(ctx, id) {
				failed = append(failed, fmt.Errorf("cancel run %s: %w", id, err))
			}
		}
	}

	return errors.Join(failed...)
}

func (l *liveSuite) completed(ctx context.Context, id string) bool {
	status, err := liveGh(ctx, "run", "view", id, "--repo", l.slug(), "--json", "status", "--jq", ".status")

	return err == nil && status == "completed"
}

// checkRulesRead stops the test unless GitHub answers the read of the default branch's rules, the
// read `ils push` and `ils merge` make, the way the repository's visibility calls for: the rules on
// a public repository, and a 403 on a private one, which both take for no rule.
func (l *liveSuite) checkRulesRead(t *testing.T) {
	t.Helper()

	private := l.private(t)
	rest := "rules/branches/" + l.trunk
	path := l.apiPath(rest)
	_, err := liveGh(t.Context(), l.apiArgs(rest)...)

	if !private {
		assert.NilError(t, err, "the read of %s on a public repository", path)

		return
	}

	exit, exited := errors.AsType[*child.ExitError](err)
	assert.Assert(t, exited && strings.Contains(exit.Stderr, "HTTP 403"),
		"the read of %s ended with %v, want a 403: the account's plan no longer refuses the rules read, "+
			"so the suite no longer exercises what ils does with that refusal", path, err)
}

// private reports whether the repository is private, as GitHub shows its visibility.
func (l *liveSuite) private(t *testing.T) bool {
	t.Helper()

	return mustGh(t, "repo", "view", l.slug(), "--json", "visibility", "--jq", ".visibility") == "PRIVATE"
}

// changeTrunk commits files on top of the default branch and pushes it.
func (l *liveSuite) changeTrunk(t *testing.T, files map[string]string) {
	t.Helper()

	dir := l.clone(t)
	liveCommit(t, dir, "live trunk change", files)
	mustGit(t, dir, "push", "--quiet", l.url, "HEAD:"+l.trunkRef())
}

// ---------------------------------------------------------------------------
// Scenario branches
// ---------------------------------------------------------------------------

// branch returns a fresh clone on the branch live/<scenario>-<token>. When the scenario ends,
// the runs it left in progress are canceled; a cancel that fails is logged, since the reset of
// the next run cancels them too.
func (l *liveSuite) branch(t *testing.T, scenario string) *liveBranch {
	t.Helper()

	token := liveToken()
	b := &liveBranch{
		suite: l, dir: l.clone(t), name: liveBranchName(scenario, token), token: token,
		deadline: time.Now().Add(liveScenarioLimit),
	}
	mustGit(t, b.dir, "switch", "--quiet", "-c", b.name)

	t.Cleanup(func() {
		ctx, cancel := liveCleanupContext(t)
		defer cancel()

		if err := l.cancelRuns(ctx); err != nil {
			t.Logf("could not cancel the runs %s left in progress: %v", b.name, err)
		}
	})

	return b
}

// liveCleanupContext returns a context limited to liveCleanupLimit that outlives the test's own,
// which has ended by the time a cleanup runs.
func liveCleanupContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(context.WithoutCancel(t.Context()), liveCleanupLimit)
}

// liveCommit writes files, each a slash-separated path and its content, into the clone at dir
// and commits everything under message.
func liveCommit(t *testing.T, dir, message string, files map[string]string) {
	t.Helper()

	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		assert.NilError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		assert.NilError(t, os.WriteFile(full, []byte(content), 0o600))
	}

	mustGit(t, dir, "add", "--all")
	mustGit(t, dir, "commit", "--quiet", "-m", message)
}

// commit commits files and a note that holds the branch's token, so that the commit differs
// from every earlier run's.
func (b *liveBranch) commit(t *testing.T, files map[string]string) {
	t.Helper()

	all := map[string]string{liveNote: b.token + "\n"}
	maps.Copy(all, files)

	liveCommit(t, b.dir, "live "+b.name, all)
}

// runOnce runs `ils <command> --json` once in the clone, launched now, and returns its result.
func (b *liveBranch) runOnce(t *testing.T, command string) liveRun {
	t.Helper()

	got := launch(t, over(git.Runner{Dir: b.dir}), command, "--json")
	t.Logf("ils %s --json on %s exited %d:\n%s%s", command, b.name, got.code, got.stdout, got.stderr)

	return liveRun{code: got.code, raw: got.stdout, shown: resulttest.Decode(t, got.stdout)}
}

// run runs `ils <command> --json` the way an agent does, again while it exits as pending, and
// returns the last result. It stops the test when the result is still pending at the deadline.
func (b *liveBranch) run(t *testing.T, command string) liveRun {
	t.Helper()

	for {
		got := b.runOnce(t, command)
		if got.code != exitPending {
			return got
		}

		assert.Assert(t, time.Now().Before(b.deadline),
			"ils %s on %s is still pending %s after the scenario began; last result:\n%s",
			command, b.name, liveScenarioLimit, got.raw)
	}
}

// push runs `ils push --json` again while it is pending and returns the last result.
func (b *liveBranch) push(t *testing.T) liveRun {
	t.Helper()

	return b.run(t, "push")
}

// merge runs `ils merge --json` again while it is pending and returns the last result.
func (b *liveBranch) merge(t *testing.T) liveRun {
	t.Helper()

	return b.run(t, "merge")
}

// onePull returns the branch's open pull request, and stops the test unless it has exactly one.
func (b *liveBranch) onePull(t *testing.T) livePull {
	t.Helper()

	out := mustGh(t, "pr", "list", "--repo", b.suite.slug(), "--head", b.name, "--state", "open",
		"--json", "number,title,body,baseRefName,headRefOid,isDraft")

	var pulls []livePull

	assert.NilError(t, json.Unmarshal([]byte(out), &pulls), "gh pr list printed:\n%s", out)
	assert.Assert(t, is.Len(pulls, 1), "open pull requests of %s", b.name)

	return pulls[0]
}

// pushedPull commits files, pushes the branch, and returns the number of its pull request.
func (b *liveBranch) pushedPull(t *testing.T, files map[string]string) string {
	t.Helper()

	b.commit(t, files)
	b.push(t)

	return strconv.Itoa(b.onePull(t).Number)
}

func (b *liveBranch) tip(t *testing.T) string {
	t.Helper()

	return mustGit(t, b.dir, "rev-parse", "HEAD")
}

// remoteTip is the commit the branch stands on in the repository on GitHub, and empty when GitHub
// no longer has the branch.
func (b *liveBranch) remoteTip(t *testing.T) string {
	t.Helper()

	return b.suite.remoteCommit(t, headRef(b.name))
}

// state is the state GitHub gives the pull request numbered number: OPEN, CLOSED or MERGED.
func (b *liveBranch) state(t *testing.T, number string) string {
	t.Helper()

	return mustGh(t, "pr", "view", number, "--repo", b.suite.slug(), "--json", "state", "--jq", ".state")
}

// checkLanded checks that the clone ended on the trunk at origin's tip, at, and that the branch is
// gone from the clone and from origin.
func (b *liveBranch) checkLanded(t *testing.T, at string) {
	t.Helper()

	assert.Check(t, is.Equal(mustGit(t, b.dir, "branch", "--show-current"), b.suite.trunk))
	assert.Check(t, is.Equal(b.tip(t), at))
	assert.Check(t, is.Equal(mustGit(t, b.dir, "branch", "--list", b.name), ""), "%s is still in the clone", b.name)
	assert.Check(t, is.Equal(b.remoteTip(t), ""), "%s is still on origin", b.name)
}

// checkFreshPull returns the branch's open pull request, which must be the only one, and checks
// that it is the one ils opens: against the trunk, on tip, titled with the branch's name.
func (b *liveBranch) checkFreshPull(t *testing.T, tip string) livePull {
	t.Helper()

	pull := b.onePull(t)
	want := livePull{Number: pull.Number, Title: b.name, Base: b.suite.trunk, Head: tip}
	assert.Check(t, is.DeepEqual(pull, want))

	return pull
}

// waitWorkflowActive stops the test unless GitHub lists the workflow at path as active before the
// deadline of the scenario.
func (b *liveBranch) waitWorkflowActive(t *testing.T, path string) {
	t.Helper()

	for {
		state := mustGh(t, b.suite.apiArgs("actions/workflows",
			"--jq", `.workflows[] | select(.path == "`+path+`") | .state`)...)
		if state == "active" {
			return
		}

		if !time.Now().Before(b.deadline) {
			t.Fatalf("GitHub does not list %s as active", path)
		}

		time.Sleep(liveRegistryPoll)
	}
}

// lines is the result's shown lines joined by newlines, so one Contains check can match across them.
func (r liveRun) lines() string {
	return strings.Join(r.shown.Lines, "\n")
}

// check returns the first check of the result in bucket, and the zero Check when there is none.
func (r liveRun) check(bucket string) result.Check {
	if r.shown.Payload == nil || r.shown.Payload.Push == nil {
		return result.Check{}
	}

	checks := r.shown.Payload.Push.Checks
	if at := slices.IndexFunc(checks, func(c result.Check) bool { return c.Bucket == bucket }); at >= 0 {
		return checks[at]
	}

	return result.Check{}
}

// liveWorkflowFile is the workflow file at path that GitHub runs on trigger, and whose one job
// runs script, one line of shell.
func liveWorkflowFile(path, name, trigger, script string) map[string]string {
	return map[string]string{path: "name: " + name + "\n" +
		"on: " + trigger + "\n" +
		"permissions: {}\n" +
		"jobs:\n" +
		"  " + liveJob + ":\n" +
		"    runs-on: ubuntu-latest\n" +
		"    timeout-minutes: 15\n" +
		"    steps:\n" +
		"      - run: " + script + "\n"}
}

// liveWorkflow is a workflow that runs script, one line of shell, on every pull request. It
// travels on the scenario's branch: for a pull request GitHub runs the workflows of its head.
func liveWorkflow(script string) map[string]string {
	return liveWorkflowFile(liveWorkflowPath, "live", "pull_request", script)
}

// liveDispatchedWorkflow is a workflow GitHub runs only when someone starts it by hand: on the
// trunk it is CI that no pull request starts.
func liveDispatchedWorkflow() map[string]string {
	return liveWorkflowFile(liveDispatchedPath, "dispatched", "workflow_dispatch", "echo started by hand")
}

// ---------------------------------------------------------------------------
// Scenarios
// ---------------------------------------------------------------------------

func TestApp_Run_AgainstALiveRepository_EndsEachScenarioAsGitHubReportsIt(t *testing.T) {
	l := newLiveSuite(t)

	// The scenarios that write to the default branch run last.
	scenarios := []struct {
		run  func(t *testing.T, l *liveSuite)
		name string
	}{
		{name: "green", run: liveGreen},
		{name: "green pushed again", run: liveGreenAgain},
		{name: "red", run: liveRed},
		{name: "no CI", run: liveNoCI},
		{name: "pending", run: livePending},
		{name: "status of a pull request", run: liveStatusOfPull},
		{name: "status of the trunk", run: liveStatusOfTrunk},
		{name: "branch", run: liveStartBranch},
		{name: "branch taken on origin", run: liveBranchTaken},
		{name: "already landed", run: liveAlreadyLanded},
		{name: "conflict", run: liveConflict},
		{name: "CI that did not fire", run: liveCIDidNotFire},
		{name: "title refused", run: liveTitleRefused},
		{name: "merged", run: liveMerged},
		{name: "merged on the web", run: liveMergedOnTheWeb},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { sc.run(t, l) })
	}
}

func liveGreen(t *testing.T, l *liveSuite) {
	b := l.branch(t, "green")
	b.commit(t, liveWorkflow(livePassing))

	got := b.push(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)

	pull := b.checkFreshPull(t, b.tip(t))
	assert.Check(t, is.Equal(got.check("pass").Name, liveJob), "no passed check named %s in:\n%s", liveJob, got.raw)
	checkMergeNext(t, l, got, pull.Number)
}

// checkMergeNext checks that a push's next step names the pull request numbered number in l's
// repository and the merge command, `ils merge`.
func checkMergeNext(t *testing.T, l *liveSuite, got liveRun, number int) {
	t.Helper()

	assert.Check(t, is.Contains(got.shown.Next, l.slug()+" "+strconv.Itoa(number)))
	assert.Check(t, is.Contains(got.shown.Next, "ils merge"))
}

func liveGreenAgain(t *testing.T, l *liveSuite) {
	b := l.branch(t, "green-again")
	b.commit(t, liveWorkflow(livePassing))
	b.push(t)
	pull := b.onePull(t)

	again := b.push(t)

	assert.Check(t, is.Equal(again.code, exitDone), "%s", again.raw)
	assert.Check(t, is.DeepEqual(b.onePull(t), pull))
}

func liveRed(t *testing.T, l *liveSuite) {
	b := l.branch(t, "red")
	marker := "live marker " + b.token
	b.commit(t, liveWorkflow(`echo "`+marker+`" && exit 1`))

	got := b.push(t)

	assert.Equal(t, got.code, exitDelivered, "%s", got.raw)

	failed := got.check("fail")
	assert.Assert(t, failed.Name != "" && failed.Link != "", "no failing check with a link in:\n%s", got.raw)
	assert.Check(t, is.Equal(failed.Name, liveJob))
	assert.Check(t, is.Contains(got.shown.Reason, liveJob))
	assert.Check(t, is.Contains(got.lines(), failed.Link))
	assert.Check(t, is.Contains(got.shown.Excerpt, marker))
	assert.Check(t, is.Contains(got.shown.Next, "ils push"))
}

func liveNoCI(t *testing.T, l *liveSuite) {
	b := l.branch(t, "no-ci")
	b.commit(t, nil)

	got := b.push(t)
	tip := b.tip(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)

	pull := b.checkFreshPull(t, tip)
	assert.Check(t, is.Equal(got.shown.Reason, ""))
	assert.Check(t, is.Contains(got.lines(), l.trunk))
	assert.Check(t, is.Contains(got.lines(), tip))
	checkMergeNext(t, l, got, pull.Number)
}

func livePending(t *testing.T, l *liveSuite) {
	b := l.branch(t, "pending")
	b.commit(t, liveWorkflow("sleep 600"))

	got := b.runOnce(t, "push")

	assert.Equal(t, got.code, exitPending, "%s", got.raw)
	assert.Check(t, is.Equal(b.onePull(t).Head, b.tip(t)))
	assert.Check(t, is.Contains(got.shown.Next, "ils push"))
}

// liveStatus runs `ils status --json` in the clone at dir, checks it ended done, and returns its
// status object.
func liveStatus(t *testing.T, dir string) *result.Status {
	t.Helper()

	got := launch(t, over(git.Runner{Dir: dir}), "status", "--json")
	t.Logf("ils status --json in %s exited %d:\n%s%s", dir, got.code, got.stdout, got.stderr)
	shown := resulttest.Decode(t, got.stdout)
	assert.Equal(t, got.code, exitDone, "%s", got.stdout)
	assert.Assert(t, shown.Payload != nil && shown.Payload.Status != nil, "no status object in:\n%s", got.stdout)

	return shown.Payload.Status
}

func liveStatusOfPull(t *testing.T, l *liveSuite) {
	b := l.branch(t, "status")
	b.commit(t, liveWorkflow(livePassing))
	b.push(t)
	number := b.onePull(t).Number

	got := liveStatus(t, b.dir)

	assert.Assert(t, got.PullRequest != nil, "no open pull request in the status of %s", b.name)
	assert.Check(t, is.Equal(got.PullRequest.Number, number))
	assert.Check(t, is.Equal(got.PullRequest.Verdict, string(forge.VerdictGreen)))
}

func liveStatusOfTrunk(t *testing.T, l *liveSuite) {
	want := result.ProtectionNone
	if l.private(t) {
		want = result.ProtectionUnavailable
	}

	got := liveStatus(t, l.clone(t))

	assert.Check(t, is.Equal(got.Standing, result.StandingTrunk))
	assert.Check(t, is.Equal(got.Protection, want))
}

func liveStartBranch(t *testing.T, l *liveSuite) {
	dir := l.clone(t)
	name := liveBranchName("branch", liveToken())

	started := launch(t, over(git.Runner{Dir: dir}), "branch", name, "--json")

	assert.Equal(t, started.code, exitDone, "%s", started.stdout)
	assert.Check(t, is.Equal(mustGit(t, dir, "symbolic-ref", "--short", "HEAD"), name))
	assert.Check(t, is.Equal(mustGit(t, dir, "rev-parse", "HEAD"), l.mustRemoteCommit(t, l.trunkRef())))
}

func liveBranchTaken(t *testing.T, l *liveSuite) {
	name := liveBranchName("branch", liveToken())
	mustGit(t, l.clone(t), "push", "--quiet", l.url, "HEAD:"+headRef(name))

	got := launch(t, over(git.Runner{Dir: l.clone(t)}), "branch", name, "--json")

	assert.Check(t, is.Equal(got.code, exitRefused), "%s", got.stdout)
	assert.Check(t, is.Contains(got.stdout, "on origin"))
}

func liveAlreadyLanded(t *testing.T, l *liveSuite) {
	b := l.branch(t, "landed")
	number := b.pushedPull(t, nil)
	mustGh(t, "pr", "merge", number, "--repo", l.slug(), "--squash")
	before := b.remoteTip(t)

	got := b.push(t)

	assert.Equal(t, got.code, exitRefused, "%s", got.raw)
	assert.Check(t, is.Contains(got.shown.Reason, b.name))
	assert.Check(t, is.Contains(got.shown.Reason, "#"+number))
	assert.Check(t, got.shown.Payload == nil, "a refusal carries a payload:\n%s", got.raw)
	assert.Check(t, is.Equal(b.remoteTip(t), before))
}

func liveConflict(t *testing.T, l *liveSuite) {
	b := l.branch(t, "conflict")
	b.commit(t, map[string]string{liveContested: "the branch's line " + b.token + "\n"})
	l.changeTrunk(t, map[string]string{liveContested: "the trunk's line " + b.token + "\n"})

	got := b.push(t)

	assert.Equal(t, got.code, exitDelivered, "%s", got.raw)
	assert.Check(t, is.Equal(b.onePull(t).Head, b.tip(t)))
	assert.Assert(t, got.shown.Payload != nil && got.shown.Payload.Push != nil, "%s", got.raw)
	assert.Check(t, is.Equal(got.shown.Payload.Push.MergeState, "conflicting"))
	assert.Check(t, is.Contains(got.shown.Next, "ils push"))
}

func liveCIDidNotFire(t *testing.T, l *liveSuite) {
	before := l.mustRemoteCommit(t, l.trunkRef())
	l.changeTrunk(t, liveDispatchedWorkflow())
	b := l.branch(t, "ci-did-not-fire")
	t.Cleanup(func() {
		ctx, cancel := liveCleanupContext(t)
		defer cancel()

		// The branch's clone holds the commit the trunk stood on before the scenario changed it.
		if _, err := liveGit(ctx, b.dir, "push", "--quiet", "--force", l.url, before+":"+l.trunkRef()); err != nil {
			t.Errorf("could not put the trunk back on %s: %v", before, err)
		}
	})
	b.commit(t, nil)
	// ils reads the trunk's workflows once; one GitHub has not registered by then would read as no
	// CI, and the scenario would test nothing.
	b.waitWorkflowActive(t, liveDispatchedPath)

	got := b.push(t)
	tip := b.tip(t)

	assert.Equal(t, got.code, exitDelivered, "%s", got.raw)

	pull := b.onePull(t)
	assert.Check(t, is.Equal(pull.Head, tip))
	assert.Check(t, is.Contains(got.shown.Reason, tip))
	assert.Check(t, is.Contains(got.shown.Reason, "#"+strconv.Itoa(pull.Number)))
	assert.Check(t, is.Contains(got.lines(), tip))
}

func liveTitleRefused(t *testing.T, l *liveSuite) {
	b := l.branch(t, "title")
	number := b.pushedPull(t, liveWorkflow(livePassing))
	before := l.remoteCommit(t, l.trunkRef())

	got := b.merge(t)

	assert.Equal(t, got.code, exitRefused, "%s", got.raw)
	assert.Check(t, is.Contains(got.shown.Reason, "#"+number))
	assert.Check(t, is.Contains(got.shown.Reason, strconv.Quote(b.name)))
	assert.Check(t, is.Equal(l.remoteCommit(t, l.trunkRef()), before))
	assert.Check(t, is.Equal(b.state(t, number), "OPEN"))
}

func liveMerged(t *testing.T, l *liveSuite) {
	b := l.branch(t, "merged")
	number := b.pushedPull(t, liveWorkflow(livePassing))
	title, body := "feat(live): land "+b.token, "Landed by the live suite as "+b.token+"."
	mustGh(t, "pr", "edit", number, "--repo", l.slug(), "--title", title, "--body", body)
	before := l.remoteCommit(t, l.trunkRef())

	got := b.merge(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)
	assert.Check(t, is.Equal(b.state(t, number), "MERGED"))

	after := l.remoteCommit(t, l.trunkRef())
	assert.Check(t, is.Equal(mustGit(t, b.dir, "rev-parse", after+"^"), before), "origin's trunk gained more than one commit")
	assert.Check(t, is.Equal(mustGit(t, b.dir, "log", "-1", "--format=%s", after), title+" (#"+number+")"))
	message := mustGit(t, b.dir, "log", "-1", "--format=%b", after)
	assert.Check(t, strings.HasPrefix(message, body), "squash commit body = %q, want it to start with %q", message, body)
	b.checkLanded(t, after)
}

func liveMergedOnTheWeb(t *testing.T, l *liveSuite) {
	b := l.branch(t, "merged-web")
	number := b.pushedPull(t, nil)
	mustGh(t, "pr", "merge", number, "--repo", l.slug(), "--squash")
	before := l.remoteCommit(t, l.trunkRef())

	got := b.merge(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)
	assert.Check(t, is.Contains(got.lines(), "#"+number))
	assert.Check(t, is.Contains(got.lines(), l.trunk))
	assert.Check(t, is.Equal(l.remoteCommit(t, l.trunkRef()), before), "ils merge made a second commit on origin's trunk")
	b.checkLanded(t, before)
}
