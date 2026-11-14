package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"mvdan.cc/sh/v3/syntax"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	// harnessClaude is the harness word of Claude Code, the one harness `ils hook` and `ils init`
	// know.
	harnessClaude = "claude"

	flagProject = landing.FlagProject

	// readTopLevel names the read of the top level of the repository a --project run installs in,
	// as it follows "read" in a result.
	readTopLevel = "the top level of the repository"

	// claudeDir is the directory Claude Code reads its settings and skills from, in the home
	// directory or at the top level of a repository.
	claudeDir = ".claude"

	// claudeConfigDir is the variable that, set and not empty, names the Claude Code directory at
	// user scope in place of claudeDir in the home directory.
	claudeConfigDir = "CLAUDE_CONFIG_DIR"

	// skillsDir is the directory under claudeDir that holds one directory per skill.
	skillsDir = "skills"

	// hookScript is the one shipped file Claude Code runs, and so the one written executable.
	hookScript = "hook.sh"

	// ilsSkill is the shipped skill that holds hookScript.
	ilsSkill = "ils"

	// guardScript is the ending of the path of the guard's hook script, whatever the directory
	// above the skills.
	guardScript = skillsDir + "/" + ilsSkill + "/" + hookScript

	fileMode   = 0o644
	scriptMode = 0o755

	// userHook and projectHook are the commands the guard's hook runs, at user scope and at
	// project scope.
	userHook    = "bash ~/" + claudeDir + "/" + guardScript
	projectHook = `bash "$CLAUDE_PROJECT_DIR"/` + claudeDir + "/" + guardScript
)

// harnessFit is how the words typed after a command that takes one harness word and no other
// word fit that shape.
type harnessFit int

const (
	// harnessGiven is one harness word ils knows, and nothing after it.
	harnessGiven harnessFit = iota
	harnessMissing
	// harnessUnknown is a first word that is no harness ils knows.
	harnessUnknown
	// harnessFollowed is a harness word ils knows with another word after it.
	harnessFollowed
)

// fitHarness returns how args, the words typed after a command that takes one harness word and no
// other word, fit that shape: the second word is the one a harnessFollowed names, the first the
// one a harnessUnknown names.
func fitHarness(args []string) harnessFit {
	switch {
	case len(args) == 0:
		return harnessMissing
	case args[0] != harnessClaude:
		return harnessUnknown
	case len(args) > 1:
		return harnessFollowed
	default:
		return harnessGiven
	}
}

// initCommand returns `ils init <harness>`, which installs the skills of the app for the harness:
// in the Claude Code directory at user scope (see userDir), or with --project at the top level of
// the work tree the working directory lies in.
// It refuses any words but one harness word, and runs the install under the display and the time
// limit of a command from launch. Outside --project there is no git dir, so the display checks the
// lines against none.
func (p *parse) initCommand() *cobra.Command {
	var project strictBool

	c := &cobra.Command{Use: initUse, Short: initShort, Long: initLong, Example: initExamples}
	extra := func(typed string) result.Phrase {
		return result.Say("%s takes one harness, one of: %s, and no other word, and %s was given", initWord, harnessClaude, typed)
	}

	c.Args = func(c *cobra.Command, args []string) error {
		typed := args
		if dash := c.ArgsLenAtDash(); dash >= 0 {
			typed = slices.Insert(slices.Clone(args), dash, wordDash)
		}

		switch fitHarness(typed) {
		case harnessMissing:
			return p.refuseFor(initWord, result.Say("%s takes the harness to set up, one of: %s", initWord, harnessClaude))
		case harnessUnknown:
			return p.refuseFor(initWord, result.Say("%s takes the harness to set up, one of: %s, and %s was given", initWord, harnessClaude, typed[0]))
		case harnessFollowed:
			return p.refuseFor(initWord, extra(typed[1]))
		default:
			return nil
		}
	}
	c.RunE = func(c *cobra.Command, _ []string) error {
		ctx, cancel := p.underTimeLimit(c.Context())
		defer cancel()

		p.displayed(ctx, "", func(shown *display) result.Result {
			return p.installClaude(ctx, shown, bool(project))
		})

		return nil
	}
	boolFlag(c.Flags(), &project, flagNames{long: flagProject},
		"Set up .claude/ at the top level of the repository the working directory is in, not your own Claude Code directory")
	p.refuseFlags(c, extra)

	return c
}

// installClaude installs every skill of the app in the Claude Code directory installDir returns,
// creating it when it is missing, then adds the permission rules and the guard's hook to the
// settings of that directory. The settings are read and checked before any skill is written, and
// written after. Each step reports its line to shown as it ends: each skill, then the hook, then
// each rule list. The settings lines describe a write, so they are reported only once it succeeds.
//
// The result holds every line reported, in order. A result of installDir ends the run; settings it
// cannot edit are a refusal; a failed read of the settings, or a failed write, ends the run broke.
// A skill directory that is a link that differs from the shipped skill does not stop the run, but
// ends it broke once the settings are written.
func (p *parse) installClaude(ctx context.Context, shown *display, project bool) result.Result {
	req := landing.Request{Command: initWord, Words: p.words, Args: []string{harnessClaude}, Project: project}

	dir, hook, refusal := p.installDir(ctx, shown, req)
	if refusal != nil {
		return *refusal
	}

	file := filepath.Join(dir, settingsFile)
	shown.Started(result.Say("Reading %s", file))

	settings, err := planSettings(dir, hook)
	if shape, ok := errors.AsType[*shapeError](err); ok {
		return *req.Refused(result.Say("%s, and the guard's setup edits it", shape.problem),
			result.NextStep("fix %s, then run %s again", file, result.Command(strings.Join(req.Words, " "))))
	}

	if err != nil {
		return installBroke(req, nil, err)
	}

	lines, linked, err := installSkills(shown, p.app.Skills, filepath.Join(dir, skillsDir))
	if err != nil {
		return installBroke(req, lines, err)
	}

	shown.Started(result.Say("Writing %s", file))

	if err := settings.write(); err != nil {
		return installBroke(req, lines, err)
	}

	for _, line := range settings.lines {
		shown.report(line.kind, line.phrase)
		lines = append(lines, line.phrase)
	}

	if len(linked) > 0 {
		broke := req.Broke(linkedReason(linked),
			result.NextStep("remove the link or update its target, then run %s again", result.Command(strings.Join(req.Words, " "))))
		broke.Lines = lines

		return *broke
	}

	return result.Result{Command: req.Command, Words: req.Words, Lines: lines, Next: result.NextNone(), Outcome: result.Done}
}

// linkedReason returns the reason of a run that found the directories of the skills names to be
// links that differ from the shipped skills.
func linkedReason(names []string) result.Phrase {
	if len(names) == 1 {
		return result.Say("the %s skill is a link that differs from the shipped skill", names[0])
	}

	return result.Say("the %s skills are links that differ from the shipped skills", strings.Join(names, ", "))
}

// installDir returns the Claude Code directory the install of req writes to and the command of the
// guard's hook in it: the one at user scope (see userDir), or under --project the one at the top
// level of the work tree the working directory lies in. It returns the result that ends the run
// instead when userDir refuses, when a project run is outside a work tree, or when the read of the
// top level fails, which ends as a landing command's read made before any write does (see
// landing.Request.Unread). The read of the top level is a step of shown.
func (p *parse) installDir(ctx context.Context, shown *display, req landing.Request) (dir, hook string, refusal *result.Result) {
	if !req.Project {
		return userDir(req, p.app.Environ)
	}

	logger := p.logger()
	shown.Started(result.Say("Reading %s", readTopLevel))

	top, found, err := git.Repo{Runner: p.loggedGit(logger)}.TopLevel(ctx)
	if err != nil {
		return "", "", req.Unread(readTopLevel, err)
	}

	if !found {
		return "", "", req.Refused(result.Say("%s installs in a repository, and the working directory is in none", flagProject),
			result.NextStep("run it again from inside a repository, or without %s", flagProject))
	}

	return filepath.Join(top, claudeDir), projectHook, nil
}

// userDir returns the Claude Code directory at user scope that environ names, and the command of
// the guard's hook in it. CLAUDE_CONFIG_DIR, set and not empty, is that directory, and the hook
// runs the script by its absolute path, quoted for bash. Otherwise it is claudeDir in the home
// directory, which HOME names. It returns the refusal of req instead when CLAUDE_CONFIG_DIR is not
// an absolute path or holds a character bash cannot be given, or when HOME is not set or empty.
func userDir(req landing.Request, environ []string) (dir, hook string, refusal *result.Result) {
	if config, _ := git.EnvValue(environ, claudeConfigDir); config != "" {
		if !filepath.IsAbs(config) {
			return "", "", req.Refused(result.Say("%s is %s, which is not an absolute path", claudeConfigDir, config),
				result.NextStep("set %s to an absolute path, or unset it", claudeConfigDir))
		}

		script, err := syntax.Quote(filepath.Join(config, guardScript), syntax.LangBash)
		if err != nil {
			return "", "", req.Refused(result.Say("%s is %s, which no bash command can name: %s", claudeConfigDir, config, err.Error()),
				result.NextStep("set %s to another path, or unset it", claudeConfigDir))
		}

		return config, "bash " + script, nil
	}

	home, _ := git.EnvValue(environ, "HOME")
	if home == "" {
		return "", "", req.Refused(result.Say("HOME is not set, and the skills install under it"),
			result.NextStep("set HOME, or run %s in a repository", result.Command(initWord+" "+harnessClaude+" "+flagProject)))
	}

	return filepath.Join(home, claudeDir), userHook, nil
}

// installBroke returns the broke result of req for err, the failure of a step of the install, with
// lines, those of the steps done before it, and with fixing what the reason names and running the
// same words again as the next step.
func installBroke(req landing.Request, lines []result.Phrase, err error) result.Result {
	broke := req.Broke(result.Say("the install broke: %s", err.Error()),
		result.NextStep("fix what the reason names, then run %s again", result.Command(strings.Join(req.Words, " "))))
	broke.Lines = lines

	return *broke
}

// installSkills writes each skill of shipped, a directory of its own at its root, to the directory
// of the same name under dir, creating the directories it needs, each as a step of shown. It
// returns the line of each skill, in order, and the names of the skills whose directory is a link
// that differs from the shipped skill. It stops at the first step that fails otherwise, with the
// lines of the skills before it and an error that names the path.
func installSkills(shown *display, shipped fs.FS, dir string) (lines []result.Phrase, linked []string, err error) {
	entries, err := fs.ReadDir(shipped, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("read the shipped skills: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		shown.Started(result.Say("Installing the %s skill", name))

		line, err := installSkill(shipped, name, filepath.Join(dir, name))
		if err != nil {
			return lines, nil, err
		}

		shown.report(line.kind, line.phrase)
		lines = append(lines, line.phrase)

		if line.kind == stepFailed {
			linked = append(linked, name)
		}
	}

	return lines, linked, nil
}

// installSkill writes every file of the skill name in shipped to dest, and returns the line that
// says it installed or updated the skill, or that every file was already in place. A dest that is a
// link is never written through: see linkedSkill.
func installSkill(shipped fs.FS, name, dest string) (stepLine, error) {
	info, err := os.Lstat(dest)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return stepLine{}, fmt.Errorf("read %s: %w", dest, err)
	}

	if existed && info.Mode()&fs.ModeSymlink != 0 {
		return linkedSkill(shipped, name, dest)
	}

	changed := false
	err = walkShipped(shipped, name, dest, func(file, target string) error {
		wrote, placeErr := placeFile(shipped, file, target)
		changed = changed || wrote

		return placeErr
	})
	if err != nil {
		return stepLine{}, fmt.Errorf("install the %s skill: %w", name, err)
	}

	switch {
	case !changed:
		return stepLine{phrase: result.Say("The %s skill at %s is already up to date", name, dest), kind: stepUnchanged}, nil
	case existed:
		return stepLine{phrase: result.Say("Updated the %s skill at %s", name, dest)}, nil
	default:
		return stepLine{phrase: result.Say("Installed the %s skill at %s", name, dest)}, nil
	}
}

// walkShipped calls visit with each file of the skill name in shipped and where it lands under dest,
// the directory of the skill, and stops at the first error.
func walkShipped(shipped fs.FS, name, dest string, visit func(file, target string) error) error {
	return fs.WalkDir(shipped, name, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		return visit(file, filepath.Join(dest, strings.TrimPrefix(file, name+"/")))
	})
}

// linkedSkill returns the line of the skill name whose directory dest is a link, naming its target:
// unchanged when every shipped file reads the same through the link, failed otherwise. It writes
// nothing.
func linkedSkill(shipped fs.FS, name, dest string) (stepLine, error) {
	target, err := os.Readlink(dest)
	if err != nil {
		return stepLine{}, fmt.Errorf("read the link %s: %w", dest, err)
	}

	same, err := matchesShipped(shipped, name, dest)
	if err != nil {
		return stepLine{}, err
	}

	if same {
		return stepLine{
			phrase: result.Say("The %s skill at %s is a link to %s, which matches the shipped skill", name, dest, target),
			kind:   stepUnchanged,
		}, nil
	}

	return stepLine{
		phrase: result.Say("The %s skill at %s is a link to %s, which differs from the shipped skill", name, dest, target),
		kind:   stepFailed,
	}, nil
}

// matchesShipped reports whether every file of the skill name in shipped reads the same under
// dest. A file that is missing, or that a link to nothing, a link to a file or a link loop keeps
// from being read, does not match; any other failed read is an error that names the path.
func matchesShipped(shipped fs.FS, name, dest string) (bool, error) {
	same := true
	err := walkShipped(shipped, name, dest, func(file, target string) error {
		want, err := fs.ReadFile(shipped, file)
		if err != nil {
			return fmt.Errorf("read the shipped %s: %w", file, err)
		}

		//nolint:gosec // G304: target is the path of a shipped file under the Claude Code directory.
		have, err := os.ReadFile(target)

		switch {
		case err == nil && bytes.Equal(have, want):
			return nil
		case err == nil, errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ELOOP):
			same = false

			return fs.SkipAll
		default:
			return fmt.Errorf("read %s: %w", target, err)
		}
	})

	return same, err
}

// placeFile writes the shipped file to dest with replaceFile, unless dest already holds the same
// content, and reports whether it wrote. hookScript is written executable, every other file not.
// A dest that is a link stays one: the file it leads to is written.
func placeFile(shipped fs.FS, file, dest string) (bool, error) {
	want, err := fs.ReadFile(shipped, file)
	if err != nil {
		return false, fmt.Errorf("read the shipped %s: %w", file, err)
	}

	// A dest that cannot be read is written over, and the write says what is wrong with it.
	//nolint:gosec // G304: dest is the path of a shipped file under the Claude Code directory.
	if have, readErr := os.ReadFile(dest); readErr == nil && bytes.Equal(have, want) {
		return false, nil
	}

	mode := os.FileMode(fileMode)
	if path.Base(file) == hookScript {
		mode = scriptMode
	}

	target, err := followLink(dest)
	if err != nil {
		return false, err
	}

	if err := replaceFile(target, want, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}

	return true, nil
}
