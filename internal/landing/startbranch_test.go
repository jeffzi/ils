package landing_test

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge/forgetest"
	"github.com/jeffzi/ils/internal/gittest"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// fresh is the name of the branch the tests start, with a capital and a slash that must stay as typed.
const fresh = "Docs/Add-Logo"

// branchRequest is the command, words and argument of `ils branch name`.
func branchRequest(name string) landing.Request {
	return landing.Request{Command: "branch", Words: []string{"branch", name}, Args: []string{name}}
}

// startBranch runs `ils branch name` on the scene.
func startBranch(t *testing.T, s *scene, name string) result.Result {
	t.Helper()

	return landing.StartBranch(t.Context(), s.deps, branchRequest(name))
}

// branchScene is a scene whose forge has no answer, so that any call to it is recorded as unscripted.
func branchScene(t *testing.T, arrange func(*gittest.Fixture)) *scene {
	t.Helper()

	return newScene(t, arrange, forgetest.Script{}, false)
}

// assertStarted fails unless HEAD is on the branch name, which stands at origin's trunk with no
// upstream, and the forge was never called.
func assertStarted(t *testing.T, s *scene, name string) {
	t.Helper()

	f := s.fixture
	assert.Check(t, is.Equal(f.Git("symbolic-ref", "--short", "HEAD"), name))
	assert.Check(t, is.Equal(f.Git("rev-parse", "refs/heads/"+name), f.OriginGit("rev-parse", trunk)))
	assert.Check(t, is.Equal(f.Git("for-each-ref", "--format=%(upstream)", "refs/heads/"+name), ""))
	assertAsked(t, s, nil)
}

// branchRefusal is the refusal of `ils branch name` a test expects: its text holds the parts of
// want, and what the repository holds is still before.
type branchRefusal struct {
	name, before, want string
}

// assertRefusedBranch fails unless got is the refusal of want, with nothing written.
func assertRefusedBranch(t *testing.T, s *scene, got result.Result, want branchRefusal) {
	t.Helper()

	assertEndedBy(t, got, s.gitDir, ended{outcome: result.Refused, req: branchRequest(want.name), text: want.want})
	assert.Check(t, is.Equal(s.written(), want.before))
	assertAsked(t, s, nil)
}

func TestStartBranch_WhenEveryCheckHolds_StartsTheBranchAtOriginsTrunk(t *testing.T) {
	t.Parallel()

	var tip string

	s := branchScene(t, func(f *gittest.Fixture) { tip = f.AdvanceOrigin(trunk) })

	got := startBranch(t, s, fresh)

	want := expect(fresh, tip, "Next:", "ils push")
	assertEndedBy(t, got, s.gitDir, ended{outcome: result.Done, req: branchRequest(fresh), text: want})
	assertStarted(t, s, fresh)
}

// startPoint is a place HEAD stands that the new branch is cut from origin's trunk as from any other.
type startPoint struct {
	arrange func(*gittest.Fixture)
	name    string
	// advance adds a commit to origin's trunk that the clone has not fetched.
	advance bool
}

func TestStartBranch_WhereverHEADStands_StartsAtOriginsTrunkAsFetched(t *testing.T) {
	t.Parallel()

	longer := "team/" + fresh
	tests := []startPoint{
		{name: "on a work branch with commits of its own", advance: true},
		{
			name: "on a local trunk behind origin's", advance: true,
			arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", trunk) },
		},
		{
			name:    "detached at origin's trunk",
			arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--detach", trunk) },
		},
		{
			name: "detached behind origin's trunk", advance: true,
			arrange: func(f *gittest.Fixture) { f.Git("switch", "--quiet", "--detach", trunk) },
		},
		{
			name: "a longer branch ends with the name, locally and on origin",
			arrange: func(f *gittest.Fixture) {
				f.Git("branch", longer, trunk)
				publishAtTrunk(f, longer)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := branchScene(t, func(f *gittest.Fixture) {
				if tt.arrange != nil {
					tt.arrange(f)
				}

				if tt.advance {
					f.AdvanceOrigin(trunk)
				}
			})

			got := startBranch(t, s, fresh)

			assertStoppedBy(t, got, result.Done, branchRequest(fresh))
			assertStarted(t, s, fresh)
		})
	}
}

func TestStartBranch_WhenGitCannotHoldTheName_RefusesNamingIt(t *testing.T) {
	t.Parallel()
	const name = "logo..draft"
	s := branchScene(t, nil)
	before := s.written()

	got := startBranch(t, s, name)

	assertRefusedBranch(t, s, got, branchRefusal{name: name, before: before, want: expect(name, "fatal:", "not a valid branch name")})
}

// takenName is a branch that takes the name the command is given.
type takenName struct {
	arrange func(*gittest.Fixture)
	name    string
	given   string
	// taker is the branch that takes the name, and where is "local" or "origin", the word the
	// refusal places it with.
	taker string
	where string
}

func TestStartBranch_WhenNameIsTaken_RefusesNamingTheBranchThatTakesIt(t *testing.T) {
	t.Parallel()

	const (
		parent = "docs"
		nested = "docs/logo"
	)

	tests := []takenName{
		{
			name: "the same name locally", given: nested, taker: nested, where: "local",
			arrange: func(f *gittest.Fixture) { f.Git("branch", nested, trunk) },
		},
		{
			name: "a path prefix locally", given: nested, taker: parent, where: "local",
			arrange: func(f *gittest.Fixture) { f.Git("branch", parent, trunk) },
		},
		{
			name: "the same name on origin", given: parent, taker: parent, where: "origin",
			arrange: func(f *gittest.Fixture) { publishAtTrunk(f, parent) },
		},
		{
			name: "a branch under it on origin", given: parent, taker: nested, where: "origin",
			arrange: func(f *gittest.Fixture) { publishAtTrunk(f, nested) },
		},
		{
			name: "a branch locally and another on origin", given: nested, taker: nested, where: "local",
			arrange: func(f *gittest.Fixture) {
				f.Git("branch", nested, trunk)
				publishAtTrunk(f, parent)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := branchScene(t, tt.arrange)
			before := s.written()

			got := startBranch(t, s, tt.given)

			assertRefusedBranch(t, s, got, branchRefusal{name: tt.given, before: before, want: expect(tt.taker, tt.where)})
		})
	}
}

// unreachedBranch is a run that origin stops answering partway through.
type unreachedBranch struct {
	name string
	// want is the parts of the refusal's text.
	want string
	// answered is how many calls origin answers before it stops.
	answered int
}

func TestStartBranch_WhenOriginStopsAnswering_RefusesSayingSo(t *testing.T) {
	t.Parallel()

	tests := []unreachedBranch{
		{
			// Origin answers the question about its trunk, and nothing after.
			name: "asked to fetch its trunk", answered: 1,
			want: expect("could not be reached", "fetch its trunk "+trunk, "Next:", "ils branch "+fresh),
		},
		{
			name: "asked for its branches", answered: 2,
			want: expect("could not be reached", "branches", "Next:", "ils branch "+fresh),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := branchScene(t, nil)
			s.stopAnswering(t, tt.answered)
			before := s.written()

			got := startBranch(t, s, fresh)

			assertRefusedBranch(t, s, got, branchRefusal{name: fresh, before: before, want: tt.want})
		})
	}
}

func TestStartBranch_WhenDetachedHEADHasCommitsOriginsTrunkLacks_RefusesListingThem(t *testing.T) {
	t.Parallel()

	var own []string

	s := branchScene(t, func(f *gittest.Fixture) {
		own = f.Commits(spare, sharpen)
		f.Detach()
		f.Git("branch", "--quiet", "--delete", "--force", spare)
	})
	before := s.written()

	got := startBranch(t, s, fresh)

	want := expect(own[0], sharpen, "`git switch -c <name>`", "ils push")
	assertRefusedBranch(t, s, got, branchRefusal{name: fresh, before: before, want: want})
}

func TestStartBranch_WhenLocalTrunkHasCommitsOriginLacks_StartsTheBranchNamingThem(t *testing.T) {
	t.Parallel()

	var ahead string

	s := branchScene(t, func(f *gittest.Fixture) { ahead = f.AdvanceTrunk() })

	got := startBranch(t, s, fresh)

	want := expect(ahead, "local trunk "+trunk)
	assertEndedBy(t, got, s.gitDir, ended{outcome: result.Done, req: branchRequest(fresh), text: want})
	assertStarted(t, s, fresh)
}

func TestStartBranch_WhenTreeHasChanges_CarriesThemOntoTheBranch(t *testing.T) {
	t.Parallel()
	s := branchScene(t, func(f *gittest.Fixture) {
		f.DirtyFile()
		f.StagedFile()
		f.UntrackedFile()
	})
	changes := s.fixture.Git("status", "--porcelain")

	got := startBranch(t, s, fresh)

	assertStoppedBy(t, got, result.Done, branchRequest(fresh))
	assertStarted(t, s, fresh)
	assert.Check(t, is.Equal(s.fixture.Git("status", "--porcelain"), changes))
}

func TestStartBranch_WhenTheSwitchWouldOverwriteAFile_RefusesNamingIt(t *testing.T) {
	t.Parallel()

	var path string

	s := branchScene(t, func(f *gittest.Fixture) { path = f.UntrackedOnTrunk() })
	before := s.written()

	got := startBranch(t, s, fresh)

	assertRefusedBranch(t, s, got, branchRefusal{name: fresh, before: before, want: expect(path)})
}

func TestStartBranch_WhenTheSwitchFailsNamingNoFile_EndsBroke(t *testing.T) {
	// Not parallel: failGit puts a failing git first on PATH with t.Setenv.
	s := branchScene(t, nil)
	failGit(t, s, []string{"switch"})

	got := startBranch(t, s, fresh)

	want := expect("Could not start the branch "+fresh, gittest.FailedSaid)
	assertEndedBy(t, got, s.gitDir, ended{outcome: result.Broke, req: branchRequest(fresh), text: want})
}

func TestStartBranch_WithoutGitHub_StartsTheBranch(t *testing.T) {
	t.Parallel()

	noTool := fails(&child.ToolMissingError{Tool: "gh", InstallURL: "https://cli.github.com"})
	tests := []struct {
		arrange func(*gittest.Fixture)
		name    string
		script  forgetest.Script
	}{
		{name: "gh is not installed", script: noTool},
		{
			name:    "origin's URL names no GitHub repository",
			arrange: func(f *gittest.Fixture) { f.SetOriginURL(filepath.ToSlash(f.OriginDir())) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScene(t, tt.arrange, tt.script, false)

			got := startBranch(t, s, fresh)

			assertStoppedBy(t, got, result.Done, branchRequest(fresh))
			assertStarted(t, s, fresh)
		})
	}
}
