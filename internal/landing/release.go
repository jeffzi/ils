package landing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/jeffzi/ils/internal/child"
	"github.com/jeffzi/ils/internal/forge"
	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/result"
)

// The reads of `ils release`, as a result names them.
const (
	readOriginTags    = "origin's tags"
	readReleaseCommit = "the first-parent history of origin's trunk"
	readLocalTag      = "the local tag of the version"
	readChangelog     = "the changelog of the release commit"
	readReleaseChecks = "the checks of the release commit"
)

// changelogPath is the file, at the root of the release commit, that holds the release's notes.
const changelogPath = "CHANGELOG.md"

// releaseSubject matches the subject of a release commit, as squashed from its pull request or not.
// Its one group is the version tag the subject names, which releasedTag holds to a version.
var releaseSubject = regexp.MustCompile(`^chore: release (v\S+?)(?: \(#\d+\))?$`)

// linkReference matches a line of the link reference definitions that end a changelog.
var linkReference = regexp.MustCompile(`^\[[^\]]+\]:\s`)

// releasedTag returns the version tag subject names, and whether subject is the subject of a
// release commit: `chore: release vX.Y.Z`, with an optional pre-release part, as squashed from its
// pull request or not.
func releasedTag(subject string) (string, bool) {
	match := releaseSubject.FindStringSubmatch(subject)
	if match == nil || !exactVersion(match[1]) {
		return "", false
	}

	return match[1], true
}

// exactVersion reports whether tag is a version tag written out in full: vX.Y.Z with an optional
// pre-release part, and no build metadata.
func exactVersion(tag string) bool {
	return semver.IsValid(tag) && semver.Canonical(tag) == tag
}

// versionTags returns the tags of tags that are versions, highest version first.
func versionTags(tags []git.Tag) []git.Tag {
	versions := slices.DeleteFunc(slices.Clone(tags), func(tag git.Tag) bool { return !semver.IsValid(tag.Name) })
	slices.SortStableFunc(versions, func(a, b git.Tag) int { return semver.Compare(b.Name, a.Name) })

	return versions
}

// Release runs `ils release <X.Y.Z>`, the version being the one word of req.Args.
//
// It refuses a version that is not X.Y.Z with an optional pre-release part before it reads
// anything. After the shared preconditions it checks, in this order and stopping at the first that
// fails: the version is above every other version tag origin holds; origin's trunk, as fetched
// now, has a release commit for the version since the previous version tag; origin's tag of the
// version, if any, is at that commit; unless it is, the local tag of the version, if any, is at
// that commit; the changelog at that commit holds notes for the version. A check that fails, an
// origin that cannot be reached, and a read the context's deadline cut short are refusals; a read
// that fails for any other reason is broke, and nothing is written before the CI gate and the
// reserve let the release go on, as gate and reserved say.
//
// The writes follow, as write says. Every result from the writes on carries the release payload.
func Release(ctx context.Context, deps Deps, req Request) result.Result {
	tag := "v" + req.Args[0]
	if !exactVersion(tag) {
		return *req.Refused(
			result.Say("%s is not a version ils can release: it takes X.Y.Z with an optional pre-release part, "+
				"such as 1.2.0 or 1.2.0-rc.1, with no leading v", req.Args[0]),
			result.NextStep("run %s with such a version", result.Command(ReleaseCommand)))
	}

	deps.started(result.Say("Checking the release"))

	ready, stop := Preconditions(ctx, deps, req)
	if stop != nil {
		return *stop
	}

	rel := releasing{Ready: ready, tag: tag}

	if stop := rel.checks(ctx, deps, req); stop != nil {
		return *stop
	}

	if stop := rel.gate(ctx, deps, req); stop != nil {
		return *stop
	}

	if stop := rel.reserved(ctx, req); stop != nil {
		return *stop
	}

	return *rel.write(ctx, deps, req)
}

// releasing is the release `ils release` checks: the version tag, what the preconditions found, and
// what the checks find on origin.
type releasing struct {
	// previous is the highest version tag origin holds below tag, empty when it holds none.
	previous git.Tag
	// onOrigin is origin's tag of the version, empty when origin holds none.
	onOrigin git.Tag
	tag      string
	commit   string
	// localTag is the commit the local tag of the version is at, empty when there is none or it was
	// not read because origin's tag is at the release commit.
	localTag string
	// notes are the release's notes, from the changelog at the release commit.
	notes string
	// verdict is how the CI gate ended, in the words of result.Release.Verdict.
	verdict string
	// url is the link of the GitHub release once it was created or found.
	url string
	// said are the lines the release says once its checks held, ahead of its end.
	said []result.Phrase
	Ready
}

// resumed reports whether origin holds the tag of the version at the release commit: an earlier run
// tagged and pushed it, so the release resumes past those writes.
func (rel *releasing) resumed() bool {
	return rel.onOrigin.Name != "" && rel.onOrigin.SHA == rel.commit
}

// version is the tag without its leading v, as the changelog and the payload name it.
func (rel *releasing) version() string {
	return strings.TrimPrefix(rel.tag, "v")
}

// prerelease reports whether the version has a pre-release part.
func (rel *releasing) prerelease() bool {
	return semver.Prerelease(rel.tag) != ""
}

// checks runs the checks of `ils release` after the shared preconditions, and returns the result
// of the first that refuses, or nil when all hold.
func (rel *releasing) checks(ctx context.Context, deps Deps, req Request) *result.Result {
	if stop := rel.readTags(ctx, deps, req); stop != nil {
		return stop
	}

	if stop := rel.findCommit(ctx, deps, req); stop != nil {
		return stop
	}

	if stop := rel.checkTags(ctx, deps, req); stop != nil {
		return stop
	}

	return rel.checkNotes(ctx, deps, req)
}

// readTags reads origin's tags, refuses a version that is not above every other version tag
// among them, and keeps the previous version tag and origin's tag of the version.
func (rel *releasing) readTags(ctx context.Context, deps Deps, req Request) *result.Result {
	tags, err := deps.Repo.OriginTags(ctx)
	if err != nil {
		return req.unreached(readOriginTags, err, result.Say("origin could not be reached to ask for its tags"))
	}

	if i := slices.IndexFunc(tags, func(tag git.Tag) bool { return tag.Name == rel.tag }); i >= 0 {
		rel.onOrigin = tags[i]
	}

	others := slices.DeleteFunc(versionTags(tags), func(tag git.Tag) bool { return tag.Name == rel.tag })
	if len(others) == 0 {
		return nil
	}

	if highest := others[0].Name; semver.Compare(highest, rel.tag) >= 0 {
		return req.Refused(
			result.Say("origin holds the version tag %s, which %s is not above", highest, rel.tag),
			result.NextStep("choose a version above %s, then run %s with it", highest, result.Command(ReleaseCommand)))
	}

	rel.previous = others[0]

	return nil
}

// findCommit fetches origin's trunk and finds the release commit on it: the newest commit on its
// first-parent history since the previous version tag whose subject is the version's release
// subject.
func (rel *releasing) findCommit(ctx context.Context, deps Deps, req Request) *result.Result {
	tip, stop := fetchTrunk(ctx, deps, req, rel.Trunk)
	if stop != nil {
		return stop
	}

	history, err := rel.history(ctx, deps, tip)
	if err != nil {
		return req.unreached(readReleaseCommit, err,
			result.Say("origin could not be reached to fetch its tag %s", rel.previous.Name))
	}

	for _, commit := range history {
		if tag, ok := releasedTag(commit.Subject); ok && tag == rel.tag {
			rel.commit = commit.SHA

			return nil
		}
	}

	subject := "chore: release " + rel.tag
	next := result.NextStep("land the release commit of %s on %s"+thenRunAgain,
		rel.tag, result.Branch(rel.Trunk), req.ilsCommand())

	if rel.previous.Name == "" {
		return req.Refused(
			result.Say("no commit on the first-parent history of origin's trunk %s has the subject %s; "+
				"origin holds no version tag below %s, so all of it was searched",
				result.Branch(rel.Trunk), subject, rel.tag),
			next)
	}

	return req.Refused(
		result.Say("no commit on the first-parent history of origin's trunk %s since %s has the subject %s",
			result.Branch(rel.Trunk), rel.previous.Name, subject),
		next)
}

// history returns, newest first, the first-parent history of tip since the previous version tag,
// or all of it when there is none.
func (rel *releasing) history(ctx context.Context, deps Deps, tip string) ([]git.Commit, error) {
	history, err := deps.Repo.FirstParentHistory(ctx, tip, rel.previous.SHA)
	if err == nil || endedEarly(err) || rel.previous.SHA == "" {
		return history, err
	}

	// The previous tag may be on a commit off the trunk that the clone lacks, and git fails a walk
	// from a commit it lacks as it fails on a broken repository. Fetching the tag brings its commit,
	// after which only a real failure fails the walk.
	if _, err := deps.Repo.FetchTag(ctx, rel.previous.Name); err != nil {
		return nil, fmt.Errorf("fetch tag %s: %w", rel.previous.Name, err)
	}

	return deps.Repo.FirstParentHistory(ctx, tip, rel.previous.SHA)
}

// checkTags refuses a tag of the version, on origin or local, at a commit other than the release
// commit. Origin's tag at the release commit is a release already tagged, whose local tag no
// longer matters.
func (rel *releasing) checkTags(ctx context.Context, deps Deps, req Request) *result.Result {
	if rel.onOrigin.Name != "" {
		if rel.resumed() {
			return nil
		}

		return req.Refused(
			result.Say("origin holds the tag %s at %s, not at the release commit %s",
				rel.tag, result.SHA(rel.onOrigin.SHA), result.SHA(rel.commit)),
			result.NextStep("stop and tell the user that origin's tag %s is at another commit", rel.tag))
	}

	local, found, err := deps.Repo.LocalTag(ctx, rel.tag)
	if err != nil {
		return req.unread(readLocalTag, err)
	}

	if !found || local == rel.commit {
		rel.localTag = local

		return nil
	}

	return req.Refused(
		result.Say("the local tag %s is at %s, not at the release commit %s",
			rel.tag, result.SHA(local), result.SHA(rel.commit)),
		result.NextStep("delete it by hand with `git tag --delete %s`"+thenRunAgain, rel.tag, req.ilsCommand()))
}

// checkNotes refuses a release commit whose changelog holds no notes for the version.
func (rel *releasing) checkNotes(ctx context.Context, deps Deps, req Request) *result.Result {
	content, found, err := deps.Repo.FileAt(ctx, rel.commit, changelogPath)
	if err != nil {
		return req.unread(readChangelog, err)
	}

	// Starting a changelog commits every later release to a section in it, so the user decides.
	if !found {
		return req.Refused(
			result.Say("the release commit %s has no %s", result.SHA(rel.commit), changelogPath),
			result.NextStep("stop and ask the user to start a %s with the notes of %s, landed in a new release commit",
				changelogPath, rel.tag))
	}

	rel.notes = releaseNotes(content, rel.version())
	if rel.notes == "" {
		return req.Refused(
			result.Say("the %s of the release commit %s has no notes for %s", changelogPath, result.SHA(rel.commit), rel.tag),
			result.NextStep("land a release commit of %s whose %s holds its notes"+thenRunAgain,
				rel.tag, changelogPath, req.ilsCommand()))
	}

	return nil
}

// releaseNotes returns the notes of version in changelog: the text after its heading, `## [X.Y.Z]`,
// up to the next `## ` heading, trimmed. A section that runs to the end of the file stops before the
// link reference definitions that end it; a definition inside the section, with text after it, is
// part of the notes. It is empty when changelog has no section for version.
func releaseNotes(changelog, version string) string {
	heading := "## [" + version + "]"

	var lines []string

	inSection := false

	for line := range strings.Lines(changelog) {
		text := strings.TrimRight(line, "\r\n")

		if !inSection {
			inSection = text == heading || strings.HasPrefix(text, heading+" ")

			continue
		}

		if strings.HasPrefix(text, "## ") {
			return strings.TrimSpace(strings.Join(lines, ""))
		}

		lines = append(lines, line)
	}

	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && !linkReference.MatchString(last) {
			break
		}

		lines = lines[:len(lines)-1]
	}

	return strings.TrimSpace(strings.Join(lines, ""))
}

// reserved ends the release pending, with nothing written, when less than mergeReserve is left
// before ctx's deadline.
func (rel *releasing) reserved(ctx context.Context, req Request) *result.Result {
	if !deadlineWithin(ctx, mergeReserve) {
		return nil
	}

	stop := req.ends(result.Pending,
		result.Say(fewerLeft+" for the release", span(mergeReserve), req.limitWords()),
		req.again())
	stop.Lines = slices.Concat(rel.said, []result.Phrase{result.Say(
		fewerLeftCapital+" for the release, so nothing was written",
		span(mergeReserve), req.limitWords())})

	return stop
}

// gate is the CI gate of the release: it waits for the checks of the release commit, every check
// deciding, on a deadline mergeReserve before ctx's, and returns the result that stops the release,
// or nil when it goes on. Green checks go on and are counted. A failed or canceled check is a
// refusal. Checks still running when the wait ends, by its deadline or an interrupt, end the
// release pending. When no check reported, a trunk with CI is a refusal, and a trunk without CI
// goes on, saying nothing verified the release commit.
//
// --force skips the gate, saying so, and so does origin's tag of the version at the release commit:
// the release was tagged, and so gated, before.
func (rel *releasing) gate(ctx context.Context, deps Deps, req Request) *result.Result {
	commit := result.SHA(rel.commit)

	if req.Force {
		rel.verdict = result.ReleaseForced
		rel.said = append(rel.said,
			result.Say("The CI gate was skipped because of --force, so nothing verified %s", commit))

		return nil
	}

	if rel.resumed() {
		rel.verdict = result.ReleaseSkipped

		return nil
	}

	deps.started(result.Say("Waiting for checks on %s", commit))

	waitCtx, cancel := beforeDeadline(ctx, mergeReserve)
	defer cancel()

	waited, err := rel.awaitChecks(waitCtx, deps)
	if err != nil {
		return req.unread(readReleaseChecks, err)
	}

	switch waited.Outcome {
	case WaitGreen:
		rel.verdict = result.ReleaseGreen
		rel.said = append(rel.said, waited.checksLine(rel.commit))

		return nil
	case WaitRed:
		return rel.redStop(req, waited)
	case WaitPending:
		return rel.pendingStop(req, waited)
	default:
		return rel.unreported(waitCtx, deps, req, waited)
	}
}

// awaitChecks reads the checks of the release commit every verdictPoll until they conclude, as the
// wait for a pull request's checks does with every check deciding: while a check is pending, a
// workflow run is still going, or for silentRetry while no run appeared. The deadline or an
// interrupt ending the wait, in a read or a poll, is WaitPending with the last verdict read, but
// for a red verdict still waiting for its failed log, which stays WaitRed. A read that fails for
// any other reason is its error.
func (rel *releasing) awaitChecks(ctx context.Context, deps Deps) (Waited, error) {
	var waited Waited

	began := time.Now()

	for {
		verdict, err := deps.Forge.CommitVerdict(ctx, rel.Repo, rel.commit)
		if err != nil {
			return waited.cutBy(err)
		}

		outcome, done := judge(verdict, nil)

		waited = Waited{Since: began, Verdict: verdict, Deciding: verdict.Deciding(), Outcome: outcome}
		waited.Passed, waited.Skipped = tally(waited.Deciding)
		deps.counted(len(waited.Deciding)-len(verdict.Pending()), len(waited.Deciding))

		// A failed check stays failed whatever runs start later.
		if done && (outcome == WaitRed || !runsMayStillAppear(verdict, began)) {
			return waited, nil
		}

		if err := pause(ctx, verdictPoll); err != nil {
			return waited.cutBy(err)
		}
	}
}

// cutBy returns how a wait for the checks of a commit that err ended stands: a red verdict still
// waiting for its failed log stays red, the deadline or an interrupt makes it WaitPending, and any
// other error is returned.
func (w Waited) cutBy(err error) (Waited, error) {
	if outcome, cutErr := cutShort(w.Outcome, err); cutErr == nil {
		w.Outcome = outcome

		return w, nil
	}

	if w.endPending(StageVerdict, err) {
		return w, nil
	}

	return Waited{}, err
}

// redStop refuses the release on the check of waited that failed or was canceled. A failed check
// carries the end of its log as the excerpt; a canceled one has no log and carries none.
func (rel *releasing) redStop(req Request, waited Waited) *result.Result {
	failed, _ := waited.Verdict.Failed()
	commit := result.SHA(rel.commit)

	if failed.Canceled {
		stop := req.Refused(result.Say("the check %s of the release commit %s was canceled", failed.Name, commit),
			result.NextStep("rerun the canceled check %s with `gh run rerun`"+thenRunAgain, failed.Name, req.ilsCommand()))
		stop.Lines = []result.Phrase{result.Say("Check %s was canceled: %s", failed.Name, result.URL(failed.Link))}

		return stop
	}

	stop := req.Refused(result.Say("the check %s of the release commit %s failed", failed.Name, commit),
		result.NextStep("fix the failing check, land a new release commit of %s"+thenRunAgain, rel.tag, req.ilsCommand()))
	stop.Lines = []result.Phrase{result.Say("Check %s failed: %s", failed.Name, result.URL(failed.Link))}
	stop.Excerpt = waited.excerpt()

	return stop
}

// pendingStop ends the release pending on a wait for its checks that ended before they concluded:
// by the deadline, or by an interrupt, which the reason names.
func (rel *releasing) pendingStop(req Request, waited Waited) *result.Result {
	commit := result.SHA(rel.commit)
	byInterrupt := waited.Cutoff == CutoffInterrupt
	pending := waited.Verdict.Pending()

	var (
		lines  []result.Phrase
		reason result.Phrase
	)

	switch {
	case waited.Since.IsZero() && byInterrupt:
		reason = result.Say(runInterrupted+" while reading %s; nothing was written", readReleaseChecks)
	case waited.Since.IsZero():
		reason = result.Say(timeLimitEnded+" while reading %s; nothing was written", req.limitWords(), readReleaseChecks)
	case silent(waited.Verdict) && byInterrupt:
		reason = result.Say("no check or status had reported for %s when "+runInterrupted, commit)
	case silent(waited.Verdict):
		reason = result.Say("no check or status had reported for %s "+timeLimitRanOut, commit, req.limitWords())
	case len(pending) == 0 && byInterrupt:
		reason = result.Say("the checks of %s had not all reported when "+runInterrupted, commit)
	case len(pending) == 0:
		reason = result.Say("the checks of %s had not all reported "+timeLimitRanOut, commit, req.limitWords())
	case byInterrupt:
		reason = result.Say("the checks of %s were still running when "+runInterrupted, commit)
	default:
		reason = result.Say("the checks of %s were still running "+timeLimitRanOut, commit, req.limitWords())
	}

	if len(pending) > 0 {
		lines = runningAges(pending)
	}

	stop := req.ends(result.Pending, reason, req.again())
	stop.Lines = lines

	return stop
}

// unreported decides a release for whose release commit no check or status reported by whether
// its trunk has CI: a trunk whose protection requires checks has CI, and otherwise the forge says.
// With CI the release is refused; without, it goes on, saying nothing verified the release commit.
func (rel *releasing) unreported(ctx context.Context, deps Deps, req Request, waited Waited) *result.Result {
	protection, err := deps.Forge.TrunkProtection(ctx, rel.Repo, rel.Trunk)
	if err != nil {
		return req.unread(readProtection, err)
	}

	without, err := waited.trunkWithoutCI(ctx, deps, rel.Ready, protection.RequiredChecks)
	if err != nil {
		return req.unread(readCI, err)
	}

	if !without {
		return req.Refused(result.Say("no check or status reported for the release commit %s", result.SHA(rel.commit)),
			result.NextStep("report that no check or status reported for the release commit, and stop"))
	}

	rel.verdict = result.ReleaseNone
	rel.said = append(rel.said, noCI(rel.Trunk, rel.commit))

	return nil
}

// write tags the release commit and pushes the tag to origin, unless origin holds it there already,
// then ends done with --tag-only, and otherwise publishes the GitHub release of the tag. Each step
// that ran says so in a line, and a write that fails is broke, saying what was written and what was
// not.
func (rel *releasing) write(ctx context.Context, deps Deps, req Request) *result.Result {
	if rel.resumed() {
		deps.say(&rel.said, result.Say("The tag %s is already on origin at the release commit %s, so nothing was tagged "+
			"or pushed", rel.tag, result.SHA(rel.commit)))
	} else if stop := rel.pushTag(ctx, deps, req); stop != nil {
		return stop
	}

	if !req.TagOnly {
		if stop := rel.publish(ctx, deps, req); stop != nil {
			return stop
		}
	}

	return rel.ended(req, req.ends(result.Done, result.Phrase{}, result.NextNone()))
}

// pushTag creates the tag of the version at the release commit, keeping the local one already
// there, and pushes it to origin. A push that fails ends as tagPushFailed words it.
func (rel *releasing) pushTag(ctx context.Context, deps Deps, req Request) *result.Result {
	commit := result.SHA(rel.commit)

	if rel.localTag == rel.commit {
		deps.say(&rel.said, result.Say("The local tag %s is already at the release commit %s, so it was kept", rel.tag, commit))
	} else {
		if err := deps.Repo.CreateTag(ctx, rel.tag, rel.commit); err != nil {
			return rel.broke(req, result.Say("could not create the tag %s", rel.tag), req.onceFixed(),
				result.Say("Could not create the tag %s at the release commit %s, so nothing was written: %s",
					rel.tag, commit, err.Error()))
		}

		deps.say(&rel.said, result.Say("Tagged the release commit %s as %s", commit, rel.tag))
	}

	deps.started(result.Say("Pushing the tag %s", rel.tag))

	err := deps.Repo.PushTag(ctx, rel.tag)
	if err == nil {
		deps.say(&rel.said, result.Say("Pushed the tag %s to origin", rel.tag))

		return nil
	}

	return rel.tagPushFailed(req, err)
}

// tagPushFailed returns the result of a push of the tag that failed with err, worded for what err
// says about origin, as pushFailed words the push of a branch. Only a push git refused, or one whose
// git never started, surely left origin without the tag; one cut short, one origin stopped
// answering, and one whose git a signal killed may have taken, and running the release again
// settles which, since it resumes once origin holds the tag at the release commit.
func (rel *releasing) tagPushFailed(req Request, err error) *result.Result {
	commit := result.SHA(rel.commit)

	if interrupted(err) {
		return rel.broke(req, result.Say(runInterrupted+" during the push of the tag %s", rel.tag), req.again(),
			result.Say("Started the push of the tag %s at %s when "+runInterrupted+"; it may or may not have taken",
				rel.tag, commit))
	}

	if timedOut(err) {
		return rel.broke(req, result.Say(timeLimitEnded+" during the push of the tag %s", req.limitWords(), rel.tag),
			req.again(),
			result.Say("Started the push of the tag %s at %s when "+timeLimitEnded+"; it may or may not have taken",
				rel.tag, commit, req.limitWords()))
	}

	if refused, ok := errors.AsType[*git.PushError](err); ok {
		line := result.Say("Git refused the push of the tag %s, made locally, so it was not pushed", rel.tag)
		if refused.Reason != "" {
			line = result.Say("Git refused the push of the tag %s, made locally, so it was not pushed: %s", rel.tag, refused.Reason)
		}

		stop := rel.broke(req, result.Say("git refused the push of the tag %s", rel.tag), req.onceFixed(), line)
		stop.Excerpt = lastLines(refused.Output())

		return stop
	}

	if _, ok := errors.AsType[*git.RemoteError](err); ok {
		return rel.broke(req, result.Say(originStopped+" during the push of the tag %s", rel.tag), req.again(),
			result.Say("Started the push of the tag %s at %s when "+originStopped+"; it may or may not have taken: %s",
				rel.tag, commit, err.Error()))
	}

	if _, ok := errors.AsType[*child.KilledError](err); ok {
		return rel.broke(req, result.Say("the push of the tag %s did not finish", rel.tag), req.again(),
			result.Say("The push of the tag %s at %s did not finish, so it may or may not have taken: %s",
				rel.tag, commit, err.Error()))
	}

	return rel.broke(req, result.Say("could not push the tag %s", rel.tag), req.onceFixed(),
		result.Say("The tag %s was made locally, and its push to origin failed: %s", rel.tag, err.Error()))
}

// publish leaves the GitHub release of the tag as it is when one exists, and otherwise creates it
// with the release's notes, as a pre-release when the version has a pre-release part. A failure is
// broke, and running again resumes past the tag already on origin. A creation the time limit or an
// interrupt cut short may still have gone through; the next run finds the release when it did.
func (rel *releasing) publish(ctx context.Context, deps Deps, req Request) *result.Result {
	deps.started(result.Say("Publishing the GitHub release of %s", rel.tag))

	link, found, err := deps.Forge.FindRelease(ctx, rel.Repo, rel.tag)
	if err == nil && found {
		rel.url = link
		deps.say(&rel.said, result.Say("The GitHub release of %s already exists, so it was left as it is: %s",
			rel.tag, result.URL(link)))

		return nil
	}

	if err == nil {
		release := forge.NewRelease{Tag: rel.tag, Notes: rel.notes, Prerelease: rel.prerelease()}
		if link, err = deps.Forge.PublishRelease(ctx, rel.Repo, release); err == nil {
			rel.url = link
			deps.say(&rel.said, result.Say("Created the GitHub release of %s: %s", rel.tag, result.URL(link)))

			return nil
		}

		switch {
		case interrupted(err):
			return rel.broke(req, result.Say(runInterrupted+" while publishing the GitHub release of %s", rel.tag), req.again(),
				result.Say("Started publishing the GitHub release of %s when "+runInterrupted+mayHaveGoneThrough, rel.tag))
		case timedOut(err):
			return rel.broke(req, result.Say(timeLimitEnded+" while publishing the GitHub release of %s", req.limitWords(), rel.tag),
				req.again(),
				result.Say("Started publishing the GitHub release of %s when "+timeLimitEnded+mayHaveGoneThrough,
					rel.tag, req.limitWords()))
		}
	}

	return rel.broke(req, result.Say("the GitHub release of %s was not created", rel.tag), req.again(),
		result.Say("The tag %s is on origin, but its GitHub release was not created: %s", rel.tag, err.Error()))
}

// broke ends the release as broke, with reason and next, and the lines the release said, then lines.
func (rel *releasing) broke(req Request, reason result.Phrase, next result.Next, lines ...result.Phrase) *result.Result {
	return rel.ended(req, req.ends(result.Broke, reason, next), lines...)
}

// ended returns stop, which ends req, with the lines the release said, then lines, and the release
// payload.
func (rel *releasing) ended(req Request, stop *result.Result, lines ...result.Phrase) *result.Result {
	stop.Lines = slices.Concat(rel.said, lines)
	stop.Payload = &result.Payload{Release: &result.Release{
		Version:    rel.version(),
		Tag:        rel.tag,
		SHA:        rel.commit,
		Verdict:    rel.verdict,
		URL:        rel.url,
		Prerelease: rel.prerelease(),
		TagOnly:    req.TagOnly,
	}}

	return stop
}
