package landing

import (
	"context"
	"errors"

	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
)

// errNoOneRepo is the failure of the identity read when origin's URLs do not name one GitHub
// repository, so there is no repository to ask GitHub about.
var errNoOneRepo = errors.New("origin's URLs do not name one GitHub repository")

// survey is the record of the reads the shared checks of a landing command make before the
// command's first write, for one run: git's version, where HEAD stands, origin and the repository
// it names, origin's trunk and its fetch, the changed tracked files, the local trunk, the branch,
// its copy on origin and its pull requests, and a detached HEAD's commits. The checks of a branch
// name `ils branch` makes, and the reads only merge or release make, ask git and GitHub directly.
//
// Each read is made at most once. The first check that asks for it makes it, and every check that
// asks after gets the answer or the failure the first got, a cut by the time limit or an interrupt
// included: two checks of one run never judge two answers of git or GitHub. A read that needs
// another read's answer asks the record for it, and fails with that read's failure, making no call
// of its own, when it failed.
//
// A read made after the command's first write never goes through the record, since a write changes
// what git and GitHub answer. The checks of a run ask in turn: the record is not safe for concurrent
// use.
type survey struct {
	got  reads
	deps Deps
}

// reads holds the answers of a survey, one per read, and one per pull request for the reads of a
// merged pull request, keyed by its number.
type reads struct {
	standing    answer[git.Standing]
	origin      answer[originRead]
	identity    answer[forge.Repo]
	trunk       answer[string]
	trunkFetch  answer[string]
	changed     answer[[]string]
	trunkAhead  answer[trunkAheadRead]
	tip         answer[tipRead]
	past        answer[[]git.Commit]
	onOrigin    answer[bool]
	copyFetch   answer[string]
	divergence  answer[git.Divergence]
	firstShared answer[firstSharedRead]
	changes     answer[[]forge.Change]
	detached    answer[[]git.Commit]
	mergedHeads map[int]*answer[string]
	carried     map[int]*answer[carriedRead]
	gitVersion  answer[struct{}]
}

// answer is one read of a survey: not made until the first ask, then the value and the failure
// that ask got, for every ask after it.
type answer[T any] struct {
	value T
	err   error
	made  bool
}

// of returns the answer of the read, which read makes on the first ask only.
func (a *answer[T]) of(read func() (T, error)) (T, error) {
	if !a.made {
		a.value, a.err = read()
		a.made = true
	}

	return a.value, a.err
}

// answerFor returns the answer kept under key in answers, adding one not yet made when there is
// none.
func answerFor[K comparable, T any](answers map[K]*answer[T], key K) *answer[T] {
	a, ok := answers[key]
	if !ok {
		a = &answer[T]{}
		answers[key] = a
	}

	return a
}

// newSurvey returns a record of one run that has made no read yet and makes its reads with deps.
func newSurvey(deps Deps) *survey {
	return &survey{
		deps: deps,
		got: reads{
			mergedHeads: map[int]*answer[string]{},
			carried:     map[int]*answer[carriedRead]{},
		},
	}
}

// originRead is what the read of origin's URLs found, as readOriginRepo returns it.
type originRead struct {
	urls    git.OriginURLs
	repo    forge.Repo
	oneRepo bool
}

// trunkAheadRead is what the read of the local trunk found, as readTrunkAhead returns it, with the
// name of the read that failed, a read it needed included.
type trunkAheadRead struct {
	ahead *aheadTrunk
	read  string
}

// tipRead is the branch's tip and whether the branch has a commit at all, as onBranch.readTip
// returns them.
type tipRead struct {
	tip       string
	committed bool
}

// firstSharedRead is what the shared-first-commit read found, with the name of the read that
// failed, a read it needed included.
type firstSharedRead struct {
	read  string
	found firstShared
}

// carriedRead is what the read of a merged pull request's head found, as onBranch.carriedBy
// returns it, with the name of the read that failed, a read it needed included.
type carriedRead struct {
	read     string
	unlanded []git.Commit
	shared   bool
}

// gitVersion fails when git is not installed, is older than the minimum, or names no readable
// version.
func (s *survey) gitVersion(ctx context.Context) error {
	_, err := s.got.gitVersion.of(func() (struct{}, error) {
		return struct{}{}, s.deps.Repo.CheckVersion(ctx)
	})

	return err
}

func (s *survey) standing(ctx context.Context) (git.Standing, error) {
	return s.got.standing.of(func() (git.Standing, error) { return s.deps.Repo.Standing(ctx) })
}

// origin returns origin's URLs with the repository they name and whether they name one.
func (s *survey) origin(ctx context.Context) (originRead, error) {
	return s.got.origin.of(func() (originRead, error) {
		urls, repo, oneRepo, err := readOriginRepo(ctx, s.deps)

		return originRead{urls: urls, repo: repo, oneRepo: oneRepo}, err
	})
}

// identity returns the repository origin's URLs name as GitHub describes it. URLs that do not name
// one repository fail it with errNoOneRepo.
func (s *survey) identity(ctx context.Context) (forge.Repo, error) {
	return s.got.identity.of(func() (forge.Repo, error) {
		origin, err := s.origin(ctx)

		switch {
		case err != nil:
			return forge.Repo{}, err
		case !origin.oneRepo:
			return forge.Repo{}, errNoOneRepo
		}

		return s.deps.Forge.Identity(ctx, origin.repo)
	})
}

func (s *survey) trunk(ctx context.Context) (string, error) {
	return s.got.trunk.of(func() (string, error) { return s.deps.Repo.Trunk(ctx) })
}

func (s *survey) trunkFetch(ctx context.Context) (string, error) {
	return s.got.trunkFetch.of(func() (string, error) {
		trunk, err := s.trunk(ctx)
		if err != nil {
			return "", err
		}

		return s.deps.Repo.Fetch(ctx, trunk)
	})
}

func (s *survey) changed(ctx context.Context) ([]string, error) {
	return s.got.changed.of(func() ([]string, error) { return s.deps.Repo.ChangedTrackedFiles(ctx) })
}

// trunkAhead returns the local trunk when it has commits origin's trunk lacks, as readTrunkAhead
// does, and the name of the read that failed with its failure.
func (s *survey) trunkAhead(ctx context.Context) (*aheadTrunk, string, error) {
	got, err := s.got.trunkAhead.of(func() (trunkAheadRead, error) {
		standing, err := s.standing(ctx)
		if err != nil {
			return trunkAheadRead{read: readStanding}, err
		}

		trunk, err := s.trunk(ctx)
		if err != nil {
			return trunkAheadRead{read: readTrunk}, err
		}

		here := standing.Kind == git.OnBranch && standing.Branch == trunk
		ahead, read, err := readTrunkAhead(ctx, s.deps, trunk, here)

		return trunkAheadRead{ahead: ahead, read: read}, err
	})

	return got.ahead, got.read, err
}

// tip returns the sha of the branch's tip, as onBranch.readTip reads it.
func (s *survey) tip(ctx context.Context) (tipRead, error) {
	return s.got.tip.of(func() (tipRead, error) {
		standing, err := s.standing(ctx)
		if err != nil {
			return tipRead{}, err
		}

		tip, committed, err := onBranch{branch: standing.Branch}.readTip(ctx, s.deps)

		return tipRead{tip: tip, committed: committed}, err
	})
}

// past returns the branch's commits past origin's trunk, oldest first, and none for a branch with
// no commit at all.
func (s *survey) past(ctx context.Context) ([]git.Commit, error) {
	return s.got.past.of(func() ([]git.Commit, error) {
		tip, err := s.tip(ctx)
		if err != nil || !tip.committed {
			return nil, err
		}

		trunk, err := s.trunk(ctx)
		if err != nil {
			return nil, err
		}

		return s.deps.Repo.CommitsPast(ctx, tip.tip, trunk)
	})
}

// worked returns the work branch as the survey read it: origin's trunk, where HEAD stands, and the
// branch's name, tip and commits past the trunk; or the name of the read that failed with its
// failure.
func (s *survey) worked(ctx context.Context) (onBranch, string, error) {
	standing, err := s.standing(ctx)
	if err != nil {
		return onBranch{}, readStanding, err
	}

	trunk, err := s.trunk(ctx)
	if err != nil {
		return onBranch{}, readTrunk, err
	}

	tip, err := s.tip(ctx)
	if err != nil {
		return onBranch{}, readTip, err
	}

	past, err := s.past(ctx)
	if err != nil {
		return onBranch{}, readCommits, err
	}

	return onBranch{
		Trunk: trunk, Standing: standing,
		branch: standing.Branch, tip: tip.tip, past: past,
	}, "", nil
}

func (s *survey) onOrigin(ctx context.Context) (bool, error) {
	return s.got.onOrigin.of(func() (bool, error) {
		standing, err := s.standing(ctx)
		if err != nil {
			return false, err
		}

		_, found, err := s.deps.Repo.RemoteBranch(ctx, standing.Branch)

		return found, err
	})
}

// copyFetch fetches origin's copy of the branch HEAD stands on, never of another branch.
func (s *survey) copyFetch(ctx context.Context) (string, error) {
	return s.got.copyFetch.of(func() (string, error) {
		standing, err := s.standing(ctx)
		if err != nil {
			return "", err
		}

		return s.deps.Repo.Fetch(ctx, standing.Branch)
	})
}

// divergence returns how far the branch's tip and origin's fetched copy of it each are past the
// other.
func (s *survey) divergence(ctx context.Context) (git.Divergence, error) {
	return s.got.divergence.of(func() (git.Divergence, error) {
		tip, err := s.tip(ctx)
		if err != nil {
			return git.Divergence{}, err
		}

		theirs, err := s.copyFetch(ctx)
		if err != nil {
			return git.Divergence{}, err
		}

		return s.deps.Repo.Divergence(ctx, tip.tip, theirs)
	})
}

// firstShared returns the other local branches that carry the branch's first commit past the
// trunk, as readFirstShared reads them against origin's fetched copy when origin has one, and the
// name of the read that failed with its failure. A branch with no commit past the trunk shares
// none.
func (s *survey) firstShared(ctx context.Context) (firstShared, string, error) {
	got, err := s.got.firstShared.of(func() (firstSharedRead, error) {
		b, read, err := s.worked(ctx)
		if err != nil || len(b.past) == 0 {
			return firstSharedRead{read: read}, err
		}

		found, err := s.onOrigin(ctx)
		if err != nil {
			return firstSharedRead{read: readOnOrigin}, err
		}

		p := pushReady{onBranch: b}
		if found {
			if p.copied, err = s.copyFetch(ctx); err != nil {
				return firstSharedRead{read: readCopy}, err
			}
		}

		shared, read, err := readFirstShared(ctx, s.deps, p)

		return firstSharedRead{found: shared, read: read}, err
	})

	return got.found, got.read, err
}

// changes returns the branch's pull requests, asked of GitHub for the repository origin names.
func (s *survey) changes(ctx context.Context) ([]forge.Change, error) {
	return s.got.changes.of(func() ([]forge.Change, error) {
		repo, err := s.identity(ctx)
		if err != nil {
			return nil, err
		}

		standing, err := s.standing(ctx)
		if err != nil {
			return nil, err
		}

		return s.deps.Forge.FindChanges(ctx, repo, standing.Branch)
	})
}

func (s *survey) mergedHead(ctx context.Context, number int) (string, error) {
	return answerFor(s.got.mergedHeads, number).of(func() (string, error) {
		return s.deps.Repo.FetchPullHead(ctx, number)
	})
}

// carried reports whether the head of the merged pull request number shares a commit past the
// trunk with the branch, and the branch's commits it lacks, as onBranch.carriedBy reads them, with
// the name of the read that failed and its failure.
func (s *survey) carried(ctx context.Context, number int) (bool, []git.Commit, string, error) {
	got, err := answerFor(s.got.carried, number).of(func() (carriedRead, error) {
		head, err := s.mergedHead(ctx, number)
		if err != nil {
			return carriedRead{read: readMergedHead}, err
		}

		b, read, err := s.worked(ctx)
		if err != nil {
			return carriedRead{read: read}, err
		}

		shared, unlanded, read, err := b.carriedBy(ctx, s.deps, head)

		return carriedRead{shared: shared, unlanded: unlanded, read: read}, err
	})

	return got.shared, got.unlanded, got.read, err
}

// detached returns a detached HEAD's commits past origin's trunk, oldest first.
func (s *survey) detached(ctx context.Context) ([]git.Commit, error) {
	return s.got.detached.of(func() ([]git.Commit, error) {
		trunk, err := s.trunk(ctx)
		if err != nil {
			return nil, err
		}

		return s.deps.Repo.CommitsPast(ctx, "HEAD", trunk)
	})
}
