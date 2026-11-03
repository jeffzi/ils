//go:build unix

package guard

import (
	"slices"
	"strings"
)

// subcommand returns the subcommand of the git invocation inv, and false when inv runs no git
// subcommand the guard can read.
func subcommand(inv Invocation) (string, bool) {
	if inv.Tool != Git || inv.Git.NoSubcommand || !inv.Git.Subcommand.Literal {
		return "", false
	}

	return inv.Git.Subcommand.Text, true
}

// ruleName returns the subcommand whose rules and options apply to the git subcommand sub:
// git stage is git add under another name.
func ruleName(sub string) string {
	if sub == "stage" {
		return "add"
	}

	return sub
}

// subcommandOptions is what git reads as options among the arguments of a subcommand, and the
// other arguments, up to the `--` after which every word is a path.
type subcommandOptions struct {
	// letters holds the letters of every short option, bundled or not.
	letters string
	long    []longOption
	// positional holds every argument that is neither an option nor the value of one, those after
	// `--` included.
	positional []Word
	// unreadable reports whether an argument outside a value slot is not a literal, so it may hold
	// any option, or whether a long option is a start of several, one of which takes a value.
	unreadable bool
}

// longOption is a long option by its name before any `=`: the whole name of the one option of
// the subcommand it names or starts, or its own name when it starts none or several.
type longOption struct {
	name string
	// attached reports whether a value follows the name after `=`.
	attached bool
	// after counts the short option letters git read before this option.
	after int
}

// startsOf reports whether the option's name is long or a start of it, which git reads as long.
func (l longOption) startsOf(long string) bool {
	return strings.HasPrefix(long, l.name)
}

// abbreviates reports whether name is long or a start of it at least shortest bytes long, which git
// reads as long.
func abbreviates(name, long string, shortest int) bool {
	return len(name) >= shortest && strings.HasPrefix(long, name)
}

// knownOptions is the options of a subcommand the guard knows. git reads the value of one that
// takes a value as the rest of a short option bundle or as the next word, whatever it is spelled
// like, and reads a start of a long option's name as that option when it starts no other.
type knownOptions struct {
	// short holds, by the name of a long option without the leading `--`, the letter of the short
	// option that spells it too.
	short         map[string]byte
	valuedLetters string
	// values and flags hold the names of the long options that take a value and of those that
	// take none, without the leading `--`.
	values, flags []string
}

// cspell:disable -- the table spells git's option names as git does.

// subcommandOptionTable lists the options of the subcommands the guard knows, one paragraph per
// subcommand: its name, then after `-` the short options that take a value, then its long
// options. These are the options git 2.55 lists with `git <subcommand> --help-all`, and for
// send-email those its script declares. A long option ending in `=` takes a value, and one
// starting with `[no-]` has a negated form too, which takes none. An option that takes a value
// only after `=` is listed as taking none. A letter and `|` before a long option name the short
// option that spells it too, where a rule reads that spelling.
const subcommandOptionTable = `
add -U
	[no-]dry-run [no-]verbose [no-]interactive [no-]patch [no-]auto-advance unified=
	inter-hunk-context= [no-]edit [no-]force [no-]update [no-]renormalize [no-]intent-to-add
	[no-]all [no-]ignore-removal [no-]refresh [no-]ignore-errors [no-]ignore-missing [no-]sparse
	[no-]chmod= [no-]warn-embedded-repo [no-]pathspec-from-file= [no-]pathspec-file-nul

am -Cp
	[no-]interactive n|no-verify verify [no-]binary [no-]3way [no-]quiet [no-]signoff [no-]utf8
	[no-]keep [no-]keep-non-patch [no-]message-id [no-]keep-cr [no-]scissors quoted-cr=
	[no-]whitespace= [no-]ignore-space-change [no-]ignore-whitespace [no-]directory= [no-]exclude=
	[no-]include= [no-]patch-format= [no-]reject [no-]resolvemsg= continue resolved skip abort quit
	show-current-patch retry allow-empty [no-]committer-date-is-author-date [no-]ignore-date
	[no-]rerere-autoupdate [no-]gpg-sign empty= [no-]rebasing

apply -pC
	exclude= include= no-add add [no-]stat [no-]allow-binary-replacement [no-]binary [no-]numstat
	[no-]summary [no-]check [no-]index [no-]intent-to-add [no-]cached [no-]unsafe-paths [no-]apply
	[no-]3way ours theirs union [no-]build-fake-ancestor= [no-]whitespace= [no-]ignore-space-change
	[no-]ignore-whitespace [no-]reverse [no-]unidiff-zero [no-]reject [no-]allow-overlap
	[no-]verbose [no-]quiet [no-]inaccurate-eof [no-]recount [no-]directory= [no-]allow-empty

branch -u
	[no-]verbose [no-]quiet [no-]track [no-]set-upstream [no-]set-upstream-to= [no-]unset-upstream
	[no-]color remotes contains= no-contains= with= without= [no-]abbrev all [no-]delete [no-]move
	[no-]omit-empty [no-]copy [no-]list [no-]show-current [no-]create-reflog [no-]edit-description
	[no-]force merged= no-merged= [no-]column [no-]sort= [no-]points-at= [no-]ignore-case
	[no-]recurse-submodules [no-]format=

clean -e
	[no-]quiet [no-]dry-run [no-]force [no-]interactive exclude=

commit -FmcCtU
	[no-]quiet [no-]verbose [no-]file= [no-]author= [no-]date= [no-]message= [no-]reedit-message=
	[no-]reuse-message= [no-]fixup= [no-]squash= [no-]reset-author [no-]trailer= [no-]signoff
	[no-]template= [no-]edit [no-]cleanup= [no-]status [no-]gpg-sign [no-]all [no-]include
	[no-]interactive [no-]patch unified= inter-hunk-context= [no-]only n|no-verify verify [no-]dry-run
	[no-]short [no-]branch [no-]ahead-behind [no-]porcelain [no-]long [no-]null [no-]amend
	no-post-rewrite post-rewrite [no-]untracked-files [no-]pathspec-from-file=
	[no-]pathspec-file-nul [no-]allow-empty [no-]allow-empty-message

config -ft
	[no-]global [no-]system [no-]local [no-]worktree [no-]file= [no-]blob= get get-all get-regexp
	get-urlmatch replace-all add unset unset-all rename-section remove-section list edit get-color
	get-colorbool [no-]null [no-]name-only [no-]show-origin [no-]show-scope [no-]show-names
	[no-]type= bool int bool-or-int bool-or-str path expiry-date [no-]default= [no-]comment=
	[no-]fixed-value [no-]includes [no-]all [no-]regexp [no-]value= [no-]url= [no-]append

difftool -tx
	[no-]gui [no-]dir-diff no-prompt prompt [no-]symlinks [no-]tool= [no-]tool-help
	[no-]trust-exit-code [no-]extcmd= no-index index

gc
	[no-]quiet [no-]prune [no-]cruft max-cruft-size= [no-]aggressive [no-]auto [no-]detach
	[no-]force [no-]keep-largest-pack [no-]expire-to= [no-]skip-foreground-tasks

merge -sXmF
	[no-]stat [no-]summary [no-]compact-summary [no-]log [no-]squash [no-]commit [no-]edit
	[no-]cleanup= [no-]ff ff-only [no-]rerere-autoupdate [no-]verify-signatures [no-]strategy=
	[no-]strategy-option= [no-]message= file= [no-]into-name= [no-]verbose [no-]quiet [no-]abort
	[no-]quit [no-]continue [no-]allow-unrelated-histories [no-]progress [no-]gpg-sign
	[no-]autostash [no-]overwrite-ignore [no-]signoff no-verify verify

pull -sXo
	[no-]verbose [no-]quiet [no-]progress [no-]recurse-submodules [no-]rebase [no-]stat [no-]summary
	[no-]compact-summary [no-]log [no-]signoff [no-]squash [no-]commit [no-]edit [no-]cleanup=
	[no-]ff ff-only [no-]verify [no-]verify-signatures [no-]autostash [no-]strategy=
	[no-]strategy-option= [no-]gpg-sign [no-]allow-unrelated-histories [no-]all [no-]append
	[no-]upload-pack= [no-]force [no-]tags [no-]prune [no-]jobs [no-]dry-run [no-]keep [no-]depth=
	[no-]shallow-since= [no-]shallow-exclude= [no-]deepen= unshallow [no-]update-shallow refmap=
	[no-]server-option= [no-]ipv4 [no-]ipv6 [no-]negotiation-restrict= [no-]negotiation-tip=
	[no-]negotiation-include= [no-]show-forced-updates [no-]set-upstream

push -o
	[no-]verbose [no-]quiet [no-]repo= [no-]all [no-]branches [no-]mirror [no-]delete [no-]tags
	[no-]dry-run [no-]porcelain [no-]force [no-]force-with-lease [no-]force-if-includes
	[no-]recurse-submodules= [no-]thin [no-]receive-pack= [no-]exec= [no-]set-upstream [no-]progress
	[no-]prune no-verify verify [no-]follow-tags [no-]signed [no-]atomic [no-]push-option= ipv4 ipv6

read-tree
	[no-]super-prefix= index-output= [no-]empty [no-]verbose [no-]trivial [no-]aggressive [no-]reset
	prefix= exclude-per-directory= [no-]dry-run no-sparse-checkout sparse-checkout [no-]debug-unpack
	[no-]recurse-submodules [no-]quiet

rebase -CxsX
	[no-]onto= [no-]keep-base no-verify verify [no-]quiet [no-]verbose no-stat stat [no-]trailer=
	[no-]signoff [no-]committer-date-is-author-date [no-]reset-author-date [no-]ignore-date
	[no-]ignore-whitespace [no-]whitespace= [no-]force-rebase no-ff ff continue skip abort quit
	edit-todo show-current-patch apply merge interactive [no-]preserve-merges [no-]rerere-autoupdate
	empty= [no-]keep-empty [no-]autosquash [no-]update-refs [no-]gpg-sign [no-]autostash [no-]exec=
	[no-]allow-empty-message [no-]rebase-merges [no-]fork-point [no-]strategy= [no-]strategy-option=
	[no-]root [no-]reschedule-failed-exec [no-]reapply-cherry-picks

repack -g
	[no-]cruft [no-]cruft-expiration= combine-cruft-below-size= max-cruft-size=
	[no-]name-hash-version= [no-]path-walk [no-]quiet [no-]local [no-]write-bitmap-index
	[no-]delta-islands [no-]unpack-unreachable= [no-]keep-unreachable [no-]window= [no-]window-memory=
	[no-]depth= [no-]threads= max-pack-size= [no-]filter= [no-]pack-kept-objects [no-]keep-pack=
	[no-]geometric= [no-]write-midx [no-]expire-to= [no-]filter-to=

restore -sU
	[no-]source= [no-]staged [no-]worktree [no-]ignore-unmerged [no-]overlay [no-]quiet
	[no-]recurse-submodules [no-]progress [no-]merge [no-]conflict= ours theirs [no-]patch unified=
	inter-hunk-context= [no-]ignore-skip-worktree-bits [no-]pathspec-from-file=
	[no-]pathspec-file-nul

rm
	[no-]dry-run [no-]quiet [no-]cached [no-]force [no-]ignore-unmatch [no-]sparse
	[no-]pathspec-from-file= [no-]pathspec-file-nul

send-email -v
	sender= from= in-reply-to= reply-to= subject= to= to-cmd= cc= bcc= sendmail-cmd= smtp-server=
	smtp-server-option= smtp-server-port= smtp-user= smtp-encryption= smtp-ssl-cert-path=
	smtp-ssl-client-cert= smtp-ssl-client-key= smtp-auth= imap-sent-folder= cc-cmd= header-cmd=
	suppress-cc= confirm= envelope-sender= transfer-encoding= 8bit-encoding= compose-encoding=
	batch-size= relogin-delay= identity= v= no-to no-cc no-bcc [no-]chain-reply-to smtp-pass
	smtp-ssl smtp-debug smtp-domain no-smtp-auth [no-]use-imap-only [no-]annotate compose quiet
	no-header-cmd [no-]suppress-from [no-]signed-off-cc [no-]signed-off-by-cc [no-]cc-cover
	[no-]to-cover dry-run [no-]thread [no-]validate novalidate [no-]mailmap [no-]use-mailmap [no-]format-patch
	force [no-]xmailer git-completion-helper [no-]outlook-id-fix no-identity dump-aliases
	translate-aliases

switch -cC
	[no-]create= [no-]force-create= [no-]guess [no-]discard-changes [no-]quiet
	[no-]recurse-submodules [no-]progress [no-]merge [no-]conflict= [no-]detach [no-]track
	[no-]force [no-]orphan= [no-]overwrite-ignore [no-]ignore-other-worktrees

symbolic-ref -m
	[no-]quiet [no-]delete [no-]short [no-]recurse

update-ref -m
	no-deref deref [no-]stdin [no-]create-reflog [no-]batch-updates
`

// cspell:enable

// optionsOf returns the options of the subcommand sub as subcommandOptionTable lists them. A
// subcommand it does not list has no options the guard knows.
func optionsOf(sub string) knownOptions {
	name := ruleName(sub)
	for paragraph := range strings.SplitSeq(subcommandOptionTable, "\n\n") {
		fields := strings.Fields(paragraph)
		if len(fields) == 0 || fields[0] != name {
			continue
		}

		var known knownOptions
		for _, word := range fields[1:] {
			known.add(word)
		}

		return known
	}

	return knownOptions{}
}

// add records the option word of subcommandOptionTable.
func (k *knownOptions) add(word string) {
	if letters, ok := strings.CutPrefix(word, "-"); ok {
		k.valuedLetters = letters

		return
	}

	if letter, long, ok := strings.Cut(word, "|"); ok {
		if k.short == nil {
			k.short = map[string]byte{}
		}

		k.short[long] = letter[0]
		word = long
	}

	word, negated := strings.CutPrefix(word, "[no-]")
	name, valued := strings.CutSuffix(word, "=")
	if valued {
		k.values = append(k.values, name)
	} else {
		k.flags = append(k.flags, name)
	}

	if negated {
		k.flags = append(k.flags, "no-"+name)
	}
}

// resolve returns the long option git reads name, a long option's name before any `=`, as: the
// option named so, or else the one option name starts, and whether that option takes a value.
// A name that starts several options is ambiguous and git refuses it; resolve reports it as
// ambiguous when one of them takes a value, because the guard cannot tell whether the next word
// is that value. Any other name comes back as it is.
func (k knownOptions) resolve(name string) (option string, valued, ambiguous bool) {
	bare := strings.TrimPrefix(name, "--")
	switch {
	case slices.Contains(k.values, bare):
		return name, true, false
	case slices.Contains(k.flags, bare):
		return name, false, false
	}

	values, flags := startedBy(k.values, bare), startedBy(k.flags, bare)
	if len(values)+len(flags) == 1 {
		return "--" + slices.Concat(values, flags)[0], len(values) == 1, false
	}

	return name, false, len(values) > 0
}

func startedBy(names []string, prefix string) []string {
	var started []string
	for _, name := range names {
		if strings.HasPrefix(name, prefix) {
			started = append(started, name)
		}
	}

	return started
}

// readArgs returns the options and other arguments among the subcommand arguments args, given the
// options the subcommand takes. A value is a value slot: whether it is a literal and what it is
// spelled like do not matter. So is a word after `--`. Any other word that is not a literal makes
// the arguments unreadable, unless it is a long option whose name before `=` is literal.
func readArgs(args []Word, known knownOptions) subcommandOptions {
	var o subcommandOptions
	for i := 0; i < len(args); i++ {
		w := args[i]
		text := w.Text
		switch {
		case w.Literal && text == "--":
			o.positional = append(o.positional, args[i+1:]...)

			return o
		case strings.HasPrefix(text, "--"):
			if o.readLong(w, known) {
				i++
			}
		case strings.HasPrefix(text, "-") && text != "-":
			takes, next := o.readBundle(text[1:], w.Literal, known.valuedLetters)
			opaqueBundle := !w.Literal && !takes
			o.unreadable = o.unreadable || opaqueBundle
			if next {
				i++
			}
		case !w.Literal:
			o.unreadable = true
		default:
			o.positional = append(o.positional, w)
		}
	}

	return o
}

// readLong records the long option w, given the options the subcommand takes, and reports
// whether its value is the next word. A word whose name before `=` is not all literal makes the
// arguments unreadable instead, and so does a name that starts several options one of which
// takes a value.
func (o *subcommandOptions) readLong(w Word, known knownOptions) (next bool) {
	name, _, attached := strings.Cut(w.Text, "=")
	if !w.Literal && !attached {
		o.unreadable = true

		return false
	}

	option, valued, ambiguous := known.resolve(name)
	o.unreadable = o.unreadable || ambiguous
	o.long = append(o.long, longOption{name: option, attached: attached, after: len(o.letters)})

	return !attached && valued
}

// readBundle records the short options of the bundle letters up to the first that takes a value,
// whose value is the rest of the bundle. It reports whether one takes a value, and whether that
// value is the next word: the letter ends a bundle that is literal.
func (o *subcommandOptions) readBundle(letters string, literal bool, valuedLetters string) (takes, next bool) {
	for j := range len(letters) {
		o.letters += letters[j : j+1]
		if strings.IndexByte(valuedLetters, letters[j]) >= 0 {
			return true, literal && j == len(letters)-1
		}
	}

	return false, false
}

// isShortBundle reports whether the text t is a bundle of short options: a single dash and at
// least one letter.
func isShortBundle(t string) bool {
	return len(t) >= 2 && t[0] == '-' && t[1] != '-'
}

// shortValue returns the value of the short option letter in the bundle w, reading the bundle's
// letters up to the first of valued, the short options that take a value: that letter takes the
// rest of the bundle as its value, so no later letter is an option. ok is false when the bundle
// does not set letter. attached is false when the value is the next word: letter ends a bundle
// that is a literal. A bundle that is not a literal holds its literal prefix, and its value is not
// a literal either.
func shortValue(w Word, letter byte, valued string) (value Word, ok, attached bool) {
	t := w.Text
	if !isShortBundle(t) {
		return Word{}, false, false
	}

	for i := 1; i < len(t); i++ {
		if t[i] == letter {
			return Word{Text: t[i+1:], Literal: w.Literal}, true, i+1 < len(t) || !w.Literal
		}

		if strings.IndexByte(valued, t[i]) >= 0 {
			break
		}
	}

	return Word{}, false, false
}

// rebaseBranch returns the branch operand of `git rebase` given args, the branch it switches to
// before it rebases, and whether args hold one: without `--root` it is the operand after the
// upstream, with it the only operand. unreadable reports whether args hold an argument the guard
// cannot read, which may be that operand.
func rebaseBranch(args []Word) (branch Word, named, unreadable bool) {
	options := readArgs(args, optionsOf("rebase"))
	at := 1
	if options.sets(0, "--root") {
		at = 0
	}

	if len(options.positional) <= at {
		return Word{}, false, options.unreadable
	}

	return options.positional[at], true, options.unreadable
}

// sets reports whether the options hold the short option letter, or a long option git reads as
// long: long itself or a start of it, ambiguous or not. A zero letter or an empty long is not
// looked for.
func (o subcommandOptions) sets(letter byte, long string) bool {
	if letter != 0 && strings.IndexByte(o.letters, letter) >= 0 {
		return true
	}

	return long != "" && slices.ContainsFunc(o.long, func(l longOption) bool { return l.startsOf(long) })
}

// setsLast reports whether the last of the short option letter, the long option long and its
// negation that the options hold is letter or long, the one git honors. A long option counts in
// its started forms too, and a start of both long and negation counts as negation.
func (o subcommandOptions) setsLast(letter byte, long, negation string) bool {
	letterAt := strings.LastIndexByte(o.letters, letter)
	for _, l := range slices.Backward(o.long) {
		if l.after <= letterAt {
			break
		}

		switch {
		case l.startsOf(negation):
			return false
		case l.startsOf(long):
			return true
		}
	}

	return letterAt >= 0
}

// opaque reports whether the options hold an argument the guard cannot read: one outside a value
// slot that is not a literal, or a positional argument that is not.
func (o subcommandOptions) opaque() bool {
	return o.unreadable || slices.ContainsFunc(o.positional, func(w Word) bool { return !w.Literal })
}

// attaches reports whether the options hold long, or a start of it, with a value attached.
func (o subcommandOptions) attaches(long string) bool {
	return slices.ContainsFunc(o.long, func(l longOption) bool { return l.attached && l.startsOf(long) })
}

// names reports whether the options hold long, or a start of it that starts none of others:
// git takes such a start for long, while one it shares with another option is ambiguous.
func (o subcommandOptions) names(long string, others ...string) bool {
	return slices.ContainsFunc(o.long, func(l longOption) bool {
		shared := slices.ContainsFunc(others, l.startsOf)

		return l.startsOf(long) && !shared
	})
}
