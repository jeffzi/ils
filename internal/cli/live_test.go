//go:build live

package cli_test

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
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
	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github"
	"github.com/jeffzi/ils/internal/result"
	"github.com/jeffzi/ils/internal/result/resulttest"
)

// The live suite runs `ils push` with the real git and the real gh against a test repository on
// GitHub. `task test:live` runs it, and `task test:setup-live` creates the repositories it needs.

const (
	// liveOriginVar holds the clone URL of the test repository the suite resets and writes to.
	liveOriginVar = "ILS_LIVE_ORIGIN"

	// liveNamePrefix starts the name of every repository the suite agrees to reset.
	liveNamePrefix = "ils-live-"

	// liveScenarioLimit is how long one scenario may run `ils push` again while it is pending.
	liveScenarioLimit = 8 * time.Minute
	// liveCleanupLimit is how long the cancels after a scenario may take.
	liveCleanupLimit = time.Minute
	// liveRegistryPoll is how long to wait between two reads of the workflows GitHub has registered.
	liveRegistryPoll = 5 * time.Second

	// liveListLimit is the most pull requests or runs one list of gh returns.
	liveListLimit = "1000"
	// liveTokenLength is how many characters of random text tell one run's branch and commits from
	// another's.
	liveTokenLength = 10

	// liveWorkflowDir is the directory GitHub reads workflows from.
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
)

// liveSuite is the test repository the suite writes to.
type liveSuite struct {
	// url is the clone URL liveOriginVar holds, and origin the repository it names.
	url    string
	origin git.Origin
	// trunk is the repository's default branch.
	trunk string
}

// liveBranch is one scenario's fresh clone, on a branch of its own.
type liveBranch struct {
	suite *liveSuite
	// deadline is when the scenario stops running `ils push` again.
	deadline time.Time
	dir      string
	name     string
	// token is in no earlier run's branch or commit.
	token string
}

// liveRun is one result of `ils push --json`.
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

	assert.NilError(t, checkLiveGh(os.Getenv(liveGhLegVar), os.Getenv(liveGhVersionVar), mustGh(t, "--version")))
	assert.NilError(t, checkLiveGit(os.Getenv(liveGitLegVar), os.Getenv(liveGitVersionVar), mustGit(t, "", "--version")))
	l.reset(t)
	l.checkRulesRead(t)

	return l
}

// slug is the repository as gh's --repo flag names it.
func (l *liveSuite) slug() string {
	return l.origin.Host + "/" + l.origin.Owner + "/" + l.origin.Name
}

// apiPath is the path of gh api for rest, a path under the repository.
func (l *liveSuite) apiPath(rest string) string {
	return "repos/" + l.origin.Owner + "/" + l.origin.Name + "/" + rest
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

// trunkRef is the full name of the default branch's ref.
func (l *liveSuite) trunkRef() string {
	return "refs/heads/" + l.trunk
}

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

	// One list per status GitHub gives a run that has not completed, so that the limit of a list
	// counts unfinished runs only.
	for _, status := range []string{"requested", "waiting", "pending", "queued", "in_progress"} {
		running, err := liveGh(ctx, "run", "list", "--repo", l.slug(), "--status", status, "--limit", liveListLimit,
			"--json", "databaseId", "--jq", ".[].databaseId")
		if err != nil {
			return err
		}

		for id := range strings.FieldsSeq(running) {
			if _, err := liveGh(ctx, "run", "cancel", id, "--repo", l.slug()); err != nil && !l.completed(ctx, id) {
				failed = append(failed, err)
			}
		}
	}

	return errors.Join(failed...)
}

func (l *liveSuite) completed(ctx context.Context, id string) bool {
	status, err := liveGh(ctx, "run", "view", id, "--repo", l.slug(), "--json", "status", "--jq", ".status")

	return err == nil && status == "completed"
}

// checkRulesRead stops the test unless GitHub refuses the read of the default branch's rules
// with a 403, the read `ils push` makes and the refusal it takes for "no required check".
func (l *liveSuite) checkRulesRead(t *testing.T) {
	t.Helper()

	rest := "rules/branches/" + l.trunk
	path := l.apiPath(rest)
	_, err := liveGh(t.Context(), l.apiArgs(rest)...)

	exit, exited := errors.AsType[*child.ExitError](err)
	assert.Assert(t, exited && strings.Contains(exit.Stderr, "HTTP 403"),
		"the read of %s ended with %v, want a 403: the account's plan no longer refuses the rules read, "+
			"so the suite no longer exercises what ils does with that refusal", path, err)
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

	token := strings.ToLower(rand.Text())[:liveTokenLength]
	b := &liveBranch{
		suite: l, dir: l.clone(t), name: "live/" + scenario + "-" + token, token: token,
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

// pushOnce runs `ils push --json` once in the clone, launched now, and returns its result.
func (b *liveBranch) pushOnce(t *testing.T) liveRun {
	t.Helper()

	got := launch(t, cli.App{Git: git.Runner{Dir: b.dir}}, "push", "--json")
	t.Logf("ils push --json on %s exited %d:\n%s%s", b.name, got.code, got.stdout, got.stderr)

	return liveRun{code: got.code, raw: got.stdout, shown: resulttest.Decode(t, got.stdout)}
}

// push runs `ils push --json` the way an agent does, again while it exits as pending, and
// returns the last result. It stops the test when the result is still pending at the deadline.
func (b *liveBranch) push(t *testing.T) liveRun {
	t.Helper()

	for {
		got := b.pushOnce(t)
		if got.code != exitPending {
			return got
		}

		assert.Assert(t, time.Now().Before(b.deadline),
			"ils push on %s is still pending %s after the scenario began; last result:\n%s", b.name, liveScenarioLimit, got.raw)
	}
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

func (b *liveBranch) tip(t *testing.T) string {
	t.Helper()

	return mustGit(t, b.dir, "rev-parse", "HEAD")
}

// remoteTip is the commit the branch stands on in the repository on GitHub.
func (b *liveBranch) remoteTip(t *testing.T) string {
	t.Helper()

	return b.suite.remoteCommit(t, "refs/heads/"+b.name)
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

func TestLive(t *testing.T) {
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
		{name: "already landed", run: liveAlreadyLanded},
		{name: "conflict", run: liveConflict},
		{name: "CI that did not fire", run: liveCIDidNotFire},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) { sc.run(t, l) })
	}
}

func liveGreen(t *testing.T, l *liveSuite) {
	b := l.branch(t, "green")
	b.commit(t, liveWorkflow("echo passed"))

	got := b.push(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)

	b.checkFreshPull(t, b.tip(t))
	assert.Check(t, is.Equal(got.check("pass").Name, liveJob), "no passed check named %s in:\n%s", liveJob, got.raw)
	assert.Check(t, is.Contains(got.shown.Next, "write the pull request's title and body"))
}

func liveGreenAgain(t *testing.T, l *liveSuite) {
	b := l.branch(t, "green-again")
	b.commit(t, liveWorkflow("echo passed"))
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
	assert.Check(t, is.Equal(got.shown.Reason, "the check "+liveJob+" failed"))
	assert.Check(t, is.Contains(got.lines(), failed.Link))
	assert.Check(t, is.Contains(got.shown.Excerpt, marker))
	assert.Check(t, is.Contains(got.shown.Next, "fix the failing check, commit, and run ils push again"))
}

func liveNoCI(t *testing.T, l *liveSuite) {
	b := l.branch(t, "no-ci")
	b.commit(t, nil)
	workflows := mustGit(t, b.dir, "ls-tree", "--name-only", "HEAD", liveWorkflowDir)
	assert.Assert(t, is.Equal(workflows, ""), "the trunk carries a workflow, so the scenario has CI to find")

	got := b.push(t)
	tip := b.tip(t)

	assert.Equal(t, got.code, exitDone, "%s", got.raw)

	pull := b.checkFreshPull(t, tip)
	assert.Check(t, is.Equal(got.shown.Reason, ""))
	assert.Check(t, is.Contains(got.lines(), "No CI is configured on "+l.trunk+", so nothing verified "+tip))
	repo, number := l.slug(), strconv.Itoa(pull.Number)
	assert.Check(t, is.Contains(got.shown.Next, "gh pr merge --repo "+repo+" "+number+" "))
	assert.Check(t, is.Contains(got.shown.Next, "--match-head-commit "+tip))
}

func livePending(t *testing.T, l *liveSuite) {
	b := l.branch(t, "pending")
	b.commit(t, liveWorkflow("sleep 600"))

	got := b.pushOnce(t)

	assert.Equal(t, got.code, exitPending, "%s", got.raw)
	assert.Check(t, is.Equal(b.onePull(t).Head, b.tip(t)))
	assert.Check(t, is.Contains(got.shown.Next, "ils push"))
}

func liveAlreadyLanded(t *testing.T, l *liveSuite) {
	b := l.branch(t, "landed")
	b.commit(t, nil)

	b.push(t)

	number := strconv.Itoa(b.onePull(t).Number)
	mustGh(t, "pr", "merge", number, "--repo", l.slug(), "--squash")

	before := b.remoteTip(t)

	got := b.push(t)

	assert.Equal(t, got.code, exitRefused, "%s", got.raw)
	assert.Check(t, is.Contains(got.shown.Reason, "the branch "+b.name+" already landed with pull request #"+number))
	assert.Check(t, is.Contains(got.shown.Next, "start a branch from origin's trunk"))
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
	assert.Check(t, is.Contains(got.shown.Reason, "the pull request conflicts with the trunk"))
	assert.Check(t, is.Contains(got.shown.Next, "merge the trunk into the branch"))
	assert.Check(t, is.Contains(got.shown.Next, "resolve the conflicts, commit, and run ils push again"))
}

func liveCIDidNotFire(t *testing.T, l *liveSuite) {
	before := l.remoteCommit(t, l.trunkRef())
	assert.Assert(t, before != "", "%s has no commit on %s to put back", l.url, l.trunkRef())

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
	unreported := " or status reported for " + tip + " on pull request #" + strconv.Itoa(pull.Number)
	assert.Check(t, is.Equal(pull.Head, tip))
	assert.Check(t, is.Equal(got.shown.Reason, "no check"+unreported))
	assert.Check(t, is.Contains(got.lines(), "No check"+unreported))
	assert.Check(t, is.Contains(got.shown.Next, "report that no check or status reported for the pushed commit, and stop"))
}
