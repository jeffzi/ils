package cli_test

import (
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/cli"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/github/ghtest"
	"github.com/jeffzi/ils/internal/gittest"
)

// No test that builds a scene is parallel: the fake `gh` is found through `GH_PATH`, which
// `go-gh` reads from the environment of the whole process.

const (
	trunk     = "develop"
	topic     = "tidy-readme"
	spare     = "spare"
	otherBase = "release"
	host      = "github.com"
	owner     = "acme"
	widgets   = "widgets"
	slug      = host + "/" + owner + "/" + widgets
	originURL = "ssh://git@" + slug + ".git"
	otherURL  = "ssh://git@" + host + "/acme/gadgets.git"

	// opened and merged are the numbers of the open and of the merged pull request.
	opened = 12
	merged = 9

	pullURL = "https://" + slug + "/pull/12"
	// runID is the workflow run that stands behind a check, and runLink is that check's link.
	runID   = "9001"
	runLink = "https://" + slug + "/actions/runs/" + runID + "/job/42"

	changeFields = "number,url,title,baseRefName,headRefOid,isDraft"
	headFields   = "headRefOid,mergeable,mergeStateStatus,isDraft,reviewDecision"

	// shortLeft is less than one poll of the checks, so that a wait for running checks ends
	// as pending right after its first read.
	shortLeft = 4900 * time.Millisecond

	exitDone      = 0
	exitBroke     = 1
	exitRefused   = 2
	exitPending   = 4
	exitDelivered = 5
)

// scenario is one state of the repository and of GitHub that `ils push` is run in.
type scenario struct {
	// arrange changes the repository, which stands on the topic branch with one commit.
	arrange func(f *gittest.Fixture)
	// script returns what gh answers, read once arrange has run.
	script func(f *gittest.Fixture) []ghtest.Call
	name   string
	// left is the time left under the bound at launch; zero leaves the whole bound.
	left time.Duration
	// want is the exit code.
	want int
}

// scene is one repository and one scripted gh, with a record of what git was asked.
type scene struct {
	fixture  *gittest.Fixture
	recorder *git.Recorder
	fake     *ghtest.Fake
	left     time.Duration
}

// newScene returns a repository on the topic branch whose origin names acme/widgets on GitHub,
// once sc has arranged it, and a gh that answers from sc's script.
func newScene(t *testing.T, sc scenario) *scene {
	t.Helper()

	f := gittest.New(t, trunk)
	f.Commits(topic, "tidy the readme")
	f.SetOriginURL(originURL)

	if sc.arrange != nil {
		sc.arrange(f)
	}

	var script []ghtest.Call
	if sc.script != nil {
		script = sc.script(f)
	}

	return &scene{fixture: f, recorder: &git.Recorder{}, fake: ghtest.Setup(t, script...), left: sc.left}
}

// app returns the cli over the scene's repository, launched so that the scene's time is left.
func (s *scene) app() cli.App {
	// Git hands the stand-in for ssh the host and then the remote command, such as
	// `git-upload-pack '<path>'`: it runs that service on the local origin, and the trailing #
	// turns both into a shell comment.
	serving := `service="${2%% *}"; git "${service#git-}" '` + filepath.ToSlash(s.fixture.OriginDir()) + `' #`
	env := append(s.fixture.Env(), "GIT_SSH_VARIANT=simple", "GIT_SSH_COMMAND="+serving)
	app := cli.App{Git: git.Runner{Dir: s.fixture.Dir(), Env: env, Recorder: s.recorder}}

	if s.left > 0 {
		app.Launch = time.Now().Add(s.left - 100*time.Second)
	}

	return app
}

// calls returns every argv git and gh received in the scene.
func (s *scene) calls() [][]string {
	return slices.Concat(s.recorder.Calls(), s.fake.Calls())
}

// publish puts origin's copy of the topic branch at the local tip, by origin's path: the scene's
// origin URL names GitHub.
func publish(f *gittest.Fixture) {
	f.Git("push", "--quiet", filepath.ToSlash(f.OriginDir()), topic)
}

// squash lands the topic branch's tip on origin's trunk as the merged pull request's squash, and
// keeps the tip as that pull request's head.
func squash(f *gittest.Fixture) {
	tip := tipOf(f)

	f.PullRef(merged, tip)
	f.SquashOnOrigin(tip, "tidy the readme (#"+strconv.Itoa(merged)+")")
}

// tipOf returns the sha HEAD is on.
func tipOf(f *gittest.Fixture) string {
	return f.Git("rev-parse", "HEAD")
}

// says scripts an exit 0 that prints stdout.
func says(stdout string, args ...string) ghtest.Call {
	return exits(ghtest.Answer{Stdout: stdout}, args...)
}

// exits scripts one answer to an argv.
func exits(answer ghtest.Answer, args ...string) ghtest.Call {
	return ghtest.Call{Args: args, Answers: []ghtest.Answer{answer}}
}

// login scripts the login check with answer.
func login(answer ghtest.Answer) ghtest.Call {
	return exits(answer, "auth", "status", "--hostname", host)
}

// identity scripts a logged-in gh and a repository that is a fork or not, with permission.
func identity(fork bool, permission string) []ghtest.Call {
	view := `{"isFork":` + strconv.FormatBool(fork) + `,"viewerPermission":"` + permission + `"}`

	return []ghtest.Call{
		login(ghtest.Answer{Stdout: "Logged in\n"}),
		says(view, "repo", "view", slug, "--json", "isFork,viewerPermission"),
	}
}

// allowed scripts a logged-in gh and a repository the user can push to.
func allowed() []ghtest.Call {
	return identity(false, "WRITE")
}

// listed scripts the answer to the list of the topic branch's pull requests in state.
func listed(state, body string) ghtest.Call {
	return exits(ghtest.Answer{Stdout: body},
		"pr", "list", "--repo", slug, "--head", topic, "--state", state, "--json", changeFields)
}

// changeJSON is one pull request of the topic branch as `gh pr list` prints it.
func changeJSON(number int, base, sha string) string {
	n := strconv.Itoa(number)

	return `[{"number":` + n + `,"url":"https://` + slug + `/pull/` + n + `","title":"` + topic +
		`","baseRefName":"` + base + `","headRefOid":"` + sha + `","isDraft":false}]`
}

// found scripts the pull requests of the topic branch: the open ones, then the merged ones.
func found(open, landed string) []ghtest.Call {
	return append(allowed(), listed("open", open), listed("merged", landed))
}

// headJSON is `gh pr view`'s answer for the pull request at sha, with no review decision.
func headJSON(sha, mergeable, status string) string {
	return `{"headRefOid":"` + sha + `","mergeable":"` + mergeable + `","mergeStateStatus":"` + status +
		`","isDraft":false,"reviewDecision":""}`
}

// checkJSON is `gh pr checks`'s answer with one check of workflow run 9001.
func checkJSON(name, bucket string) string {
	return `[{"name":"` + name + `","bucket":"` + bucket + `","workflow":"CI","link":"` + runLink + `","state":"X"}]`
}

// verdict is what GitHub says of pull request 12 once it is open.
type verdict struct {
	// rules is the answer to the read of the trunk's rules.
	rules string
	// head is the answer to `gh pr view`.
	head string
	// run is the status of the one workflow run on the commit.
	run string
	// checks is the answer to `gh pr checks`.
	checks ghtest.Answer
}

// passing is a mergeable pull request at sha whose one check passed, on a trunk with no rule.
func passing(sha string) verdict {
	return verdict{
		rules: "[]", head: headJSON(sha, "MERGEABLE", "CLEAN"), run: "completed",
		checks: ghtest.Answer{Stdout: checkJSON("build", "pass")},
	}
}

// calls scripts the reads of v for the commit sha.
func (v verdict) calls(sha string) []ghtest.Call {
	run := `[{"databaseId":9001,"status":"` + v.run + `","event":"pull_request","name":"Tidy","workflowName":"CI"}]`

	return []ghtest.Call{
		says(v.rules, "api", "--hostname", host, "repos/acme/widgets/rules/branches/"+trunk),
		says(v.head, "pr", "view", "--repo", slug, "12", "--json", headFields),
		exits(v.checks, "pr", "checks", "--repo", slug, "12", "--json", "name,bucket,workflow,link,state"),
		says(run, "run", "list", "--repo", slug, "--commit", sha, "--json", "databaseId,status,event,name,workflowName"),
		says("build\tRun tests\tFAIL\n", "run", "view", "--repo", slug, "9001", "--log-failed"),
	}
}

// firstPush scripts a branch with no pull request yet, and then the verdict judge gives.
func firstPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)
		opening := says(pullURL+"\n",
			"pr", "create", "--repo", slug, "--title", topic, "--body", "", "--base", trunk, "--head", topic)

		return slices.Concat(found("[]", "[]"), []ghtest.Call{opening}, judge(sha).calls(sha))
	}
}

// laterPush scripts a branch whose pull request 12 is open at its tip, and then the verdict
// judge gives.
func laterPush(judge func(sha string) verdict) func(*gittest.Fixture) []ghtest.Call {
	return func(f *gittest.Fixture) []ghtest.Call {
		sha := tipOf(f)

		return slices.Concat(found(changeJSON(opened, trunk, sha), "[]"), judge(sha).calls(sha))
	}
}

// only scripts the same answers whatever the repository holds.
func only(calls ...ghtest.Call) func(*gittest.Fixture) []ghtest.Call {
	return func(*gittest.Fixture) []ghtest.Call { return calls }
}

func greenFirstPush() scenario {
	return scenario{name: "green on a first push", script: firstPush(passing), want: exitDone}
}

func greenSecondRun() scenario {
	return scenario{name: "second run on a pushed tip", arrange: publish, script: laterPush(passing), want: exitDone}
}

func redWithLog() scenario {
	red := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "MERGEABLE", "UNSTABLE")
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "fail"), Code: 1}

		return v
	}

	return scenario{name: "red with a failed log", script: firstPush(red), want: exitDelivered}
}

func blockedByMissingCheck() scenario {
	blocked := func(sha string) verdict {
		v := passing(sha)
		v.rules = `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"build"},{"context":"test"}]}}]`
		v.head = headJSON(sha, "MERGEABLE", "BLOCKED")

		return v
	}

	return scenario{name: "blocked by a required check that never reported", script: firstPush(blocked), want: exitDelivered}
}

func conflicting() scenario {
	conflict := func(sha string) verdict {
		v := passing(sha)
		v.head = headJSON(sha, "CONFLICTING", "DIRTY")

		return v
	}

	return scenario{name: "conflicting with the trunk", script: firstPush(conflict), want: exitDelivered}
}

func pendingAtTheBound() scenario {
	running := func(sha string) verdict {
		v := passing(sha)
		v.run = "in_progress"
		v.checks = ghtest.Answer{Stdout: checkJSON("build", "pending"), Code: 8}

		return v
	}

	return scenario{
		name: "checks still running at the bound", arrange: publish, script: laterPush(running),
		left: shortLeft, want: exitPending,
	}
}

func onTheTrunk() scenario {
	return scenario{
		name: "on the trunk", script: only(allowed()...), want: exitRefused,
		arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", trunk) },
	}
}

func failedRead() scenario {
	down := exits(ghtest.Answer{Stderr: "gh: Server Error (HTTP 500)\n", Code: 1},
		"pr", "list", "--repo", slug, "--head", topic, "--state", "open", "--json", changeFields)

	return scenario{name: "a read of GitHub fails", script: only(append(allowed(), down)...), want: exitBroke}
}

// standingRefusals are the refusals decided before the branch's commits are read.
func standingRefusals() []scenario {
	loggedOut := login(ghtest.Answer{Stderr: "You are not logged into any GitHub hosts.\n", Code: 1})

	return []scenario{
		{name: "no origin", arrange: (*gittest.Fixture).RemoveOrigin, want: exitRefused},
		{
			name: "origin's two addresses differ", want: exitRefused,
			arrange: func(f *gittest.Fixture) { f.SetPushURL(otherURL) },
		},
		{name: "not logged in", script: only(loggedOut), want: exitRefused},
		{name: "a fork", script: only(identity(true, "WRITE")...), want: exitRefused},
		{name: "no right to push", script: only(identity(false, "READ")...), want: exitRefused},
		onTheTrunk(),
		{name: "detached", arrange: (*gittest.Fixture).Detach, script: only(allowed()...), want: exitRefused},
		{
			name: "tracked file changed", script: only(allowed()...), want: exitRefused,
			arrange: func(f *gittest.Fixture) { f.DirtyFile() },
		},
	}
}

// commitRefusals are the refusals decided from the branch's commits and origin's copy of it.
func commitRefusals() []scenario {
	refused := func(name string, arrange func(f *gittest.Fixture)) scenario {
		return scenario{name: name, arrange: arrange, script: only(allowed()...), want: exitRefused}
	}

	return []scenario{
		refused("no commit past the trunk", func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--create", spare, trunk) }),
		refused("no commit at all", func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--orphan", spare) }),
		refused("cut from another branch", func(f *gittest.Fixture) { f.Sibling(spare, topic) }),
		refused("behind origin's copy", func(f *gittest.Fixture) {
			publish(f)
			f.AdvanceOrigin(topic)
		}),
		refused("diverged from origin's copy", func(f *gittest.Fixture) {
			publish(f)
			f.Diverge(topic)
		}),
	}
}

// changeRefusals are the refusals decided from the pull requests GitHub finds for the branch.
func changeRefusals() []scenario {
	landed := only(found("[]", changeJSON(merged, trunk, "0a1b2c3"))...)

	return []scenario{
		{
			name: "open pull request against another base", want: exitRefused,
			script: only(found(changeJSON(opened, otherBase, "0a1b2c3"), "[]")...),
		},
		{name: "merged pull request with nothing unlanded", arrange: squash, script: landed, want: exitRefused},
		{
			name: "merged pull request with commits made since", script: landed, want: exitRefused,
			arrange: func(f *gittest.Fixture) {
				squash(f)
				f.Commits(topic, "fix a typo")
			},
		},
	}
}

// scenarios are the runs of `ils push` whose calls the usage must name.
func scenarios() []scenario {
	ends := []scenario{
		greenFirstPush(), greenSecondRun(), redWithLog(), blockedByMissingCheck(), conflicting(),
		pendingAtTheBound(), failedRead(),
	}

	return slices.Concat(ends, standingRefusals(), commitRefusals(), changeRefusals())
}

// section returns the indented lines of usage under heading, trimmed, up to the first line
// that is not indented.
func section(t *testing.T, usage, heading string) []string {
	t.Helper()

	_, after, ok := strings.Cut(usage, "\n"+heading+"\n")
	assert.Assert(t, ok, "usage has no line %q:\n%s", heading, usage)

	var lines []string

	for line := range strings.Lines(after) {
		if !strings.HasPrefix(line, "  ") {
			break
		}

		lines = append(lines, strings.TrimSpace(line))
	}

	return lines
}

// placeholder is a name that stands, in a usage line, for a value known only at run time.
var placeholder = regexp.MustCompile(`<(name|trunk|N|sha|r|host|owner|repo|id)>`)

// kinds holds, for each placeholder, the pattern of the values of its kind that the scenarios
// hold.
var kinds = map[string]string{
	"<name>":  "(?:" + topic + "|" + spare + ")",
	"<trunk>": trunk,
	"<N>":     "(?:" + strconv.Itoa(opened) + "|" + strconv.Itoa(merged) + ")",
	"<sha>":   "[0-9a-f]{40}",
	"<r>":     regexp.QuoteMeta(slug),
	"<host>":  regexp.QuoteMeta(host),
	"<owner>": owner,
	"<repo>":  widgets,
	"<id>":    runID,
}

// shape returns the pattern of the texts that printed stands for: itself, with each
// placeholder open only to the values of its own kind.
func shape(printed string) string {
	return placeholder.ReplaceAllStringFunc(regexp.QuoteMeta(printed), func(name string) string { return kinds[name] })
}

// words splits a usage line into its arguments as a shell would: at the spaces outside single
// quotes, with the quotes dropped, so that two quotes alone are the empty argument.
func words(line string) []string {
	var (
		all     []string
		word    strings.Builder
		quoted  bool
		started bool
	)

	for _, r := range line + " " {
		switch {
		case r == '\'':
			quoted, started = !quoted, true
		case r != ' ' || quoted:
			word.WriteRune(r)

			started = true
		case started:
			all = append(all, word.String())
			word.Reset()

			started = false
		}
	}

	return all
}

// stands reports whether the usage line stands for argv: as many arguments, each one equal to
// its argument or matching it once the placeholders are filled.
func stands(line string, argv []string) bool {
	printed := words(line)
	if len(printed) != len(argv) {
		return false
	}

	for i, word := range printed {
		if !regexp.MustCompile("^(?:" + shape(word) + ")$").MatchString(argv[i]) {
			return false
		}
	}

	return true
}

// pushUsage returns what `ils push --help` prints.
func pushUsage(t *testing.T) string {
	t.Helper()

	got := launch(t, outside(t), "push", "--help")
	assert.Equal(t, got.code, 0, "stderr:\n%s", got.stderr)

	return got.stdout
}

// pushed runs `ils push` in a scene of sc, and fails the test unless it exits as sc wants.
func pushed(t *testing.T, sc scenario) (*scene, ran) {
	t.Helper()

	s := newScene(t, sc)

	got := launch(t, s.app(), "push")
	assert.Equal(t, got.code, sc.want, "stdout:\n%s\nstderr:\n%s", got.stdout, got.stderr)

	return s, got
}

func TestUsage_Push_ByHandIsTheSequencePushRan(t *testing.T) {
	lines := section(t, pushUsage(t), "By hand:")

	var calls [][]string

	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			s, _ := pushed(t, sc)
			calls = append(calls, s.calls()...)
		})
	}

	for _, argv := range calls {
		assert.Check(t, slices.ContainsFunc(lines, func(line string) bool { return stands(line, argv) }), "push ran %q, which no line under \"By hand:\" stands for", argv)
	}

	for _, line := range lines {
		assert.Check(t, slices.ContainsFunc(calls, func(argv []string) bool { return stands(line, argv) }), "usage line %q under \"By hand:\" stands for no call push ran", line)
	}
}

func TestUsage_Push_ByHandNamesTheReadOfTheGitDir(t *testing.T) {
	lines := section(t, pushUsage(t), "By hand:")

	assert.Check(t, is.Contains(lines, "git rev-parse --absolute-git-dir"))
}

func TestUsage_Push_ThenByHandIsWhatTheGreenNextNames(t *testing.T) {
	lines := section(t, pushUsage(t), "Then, by hand:")

	s, green := pushed(t, greenFirstPush())

	_, next, _ := strings.Cut(green.stdout, "\nNext: ")
	blank := strings.NewReplacer(slug, "<r>", tipOf(s.fixture), "<sha>", strconv.Itoa(opened), "<N>")

	var named []string

	for i, part := range strings.Split(next, "`") {
		if i%2 == 1 {
			named = append(named, blank.Replace(part))
		}
	}

	assert.Check(t, is.DeepEqual(lines, named))

	wantStarts := []string{
		"gh pr edit --repo <r> <N> --title ",
		"gh pr merge --repo <r> <N> --squash --delete-branch --match-head-commit <sha> --subject ",
	}
	assert.Assert(t, is.Len(lines, len(wantStarts)))

	for i, line := range lines {
		assert.Check(t, strings.HasPrefix(line, wantStarts[i]), "line %q under \"Then, by hand:\", want it to start with %q", line, wantStarts[i])
		assert.Check(t, is.Contains(line, " --body "))
	}
}
