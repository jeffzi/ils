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

	"github.com/spf13/cobra"

	"github.com/jeffzi/ils/internal/git"
	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

const (
	// harnessClaude is the harness word of Claude Code, the one harness `ils hook` and `ils init`
	// know.
	harnessClaude = "claude"

	flagProject = "--project"

	// claudeDir is the directory Claude Code reads its settings and skills from, in HOME or at the
	// top level of a repository.
	claudeDir = ".claude"

	// skillsDir is the directory under claudeDir that holds one directory per skill.
	skillsDir = "skills"

	// hookScript is the one shipped file Claude Code runs, and so the one written executable.
	hookScript = "hook.sh"

	// ilsSkill is the shipped skill that holds hookScript.
	ilsSkill = "ils"

	// guardScript is the ending of the path of the guard's hook script, whatever the directory
	// above the skills.
	guardScript = skillsDir + "/" + ilsSkill + "/" + hookScript

	dirMode    = 0o750
	fileMode   = 0o644
	scriptMode = 0o755

	// guardUnsupported is the reason the guard refuses every run outside macOS and Linux.
	guardUnsupported = "the guard runs on macOS and Linux"

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
// under HOME, or with --project at the top level of the work tree the working directory lies in.
// It refuses any words but one harness word, and keeps the result of the install, under the time
// limit of a command from launch, as the result of the run.
func (p *parse) initCommand() *cobra.Command {
	var project strictBool

	c := initHeader()
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

		r := p.installClaude(ctx, bool(project))
		p.result = &r

		return nil
	}
	boolFlag(c.Flags(), &project, flagNames{long: flagProject},
		"Set up .claude/ at the top level of the repository the working directory is in, not under HOME")
	p.refuseFlags(c, extra)

	return c
}

// installClaude installs every skill of the app in the Claude Code directory, the one under HOME,
// or with project the one at the top level of the work tree, creating it when it is missing, then
// adds the permission rules and, where the guard runs, its hook to the settings of that
// directory. The settings are read and checked before any skill is written, and written after.
// The result has one line per skill installed, updated, or left as it is because its directory is
// a link, and one per change to the settings; or one line saying everything was in place. Where
// the guard does not run, a last line says where it does. A HOME that is not set, a project run
// outside a work tree, or settings it cannot edit is a refusal; a failed read of git or of the
// settings, or a failed write, ends the run broke.
func (p *parse) installClaude(ctx context.Context, project bool) result.Result {
	req := landing.Request{Command: initWord, Words: p.words}

	dir, refusal := p.installDir(ctx, req, project)
	if refusal != nil {
		return *refusal
	}

	guardRuns := guardSupported
	if p.app.guardRuns != nil {
		guardRuns = *p.app.guardRuns
	}

	hook := ""
	switch {
	case !guardRuns:
	case project:
		hook = projectHook
	default:
		hook = userHook
	}

	settings, err := planSettings(dir, hook)
	if shape, ok := errors.AsType[*shapeError](err); ok {
		return *req.Refused(result.Say("%s, and the guard's setup edits it", shape.problem),
			result.NextStep("fix %s, then run %s again", filepath.Join(dir, settingsFile), result.Command(strings.Join(req.Words, " "))))
	}

	if err != nil {
		return installBroke(req, err)
	}

	lines, err := installSkills(p.app.Skills, filepath.Join(dir, skillsDir))
	if err != nil {
		return installBroke(req, err)
	}

	if err := settings.write(); err != nil {
		return installBroke(req, err)
	}

	lines = append(lines, settings.lines...)
	if len(lines) == 0 {
		lines = []result.Phrase{result.Say("Everything was already in place.")}
	}

	if !guardRuns {
		lines = append(lines, result.Say("Added no hook: "+guardUnsupported))
	}

	return result.Result{Command: req.Command, Words: req.Words, Lines: lines, Next: result.NextNone(), Outcome: result.Done}
}

// installDir returns the Claude Code directory the install writes to: the one under HOME, or with
// project the one at the top level of the work tree the working directory lies in. It returns the
// result that ends the run instead when HOME is not set or empty, when a project run is outside a
// work tree, or when the read of git fails.
func (p *parse) installDir(ctx context.Context, req landing.Request, project bool) (string, *result.Result) {
	if !project {
		home, _ := git.EnvValue(p.app.Environ, "HOME")
		if home == "" {
			return "", req.Refused(result.Say("HOME is not set, and the skills install under it"),
				result.NextStep("set HOME, or run %s in a repository", result.Command(initWord+" "+harnessClaude+" "+flagProject)))
		}

		return filepath.Join(home, claudeDir), nil
	}

	logger := p.logger(p.app.profile(p.app.StderrTerminal, p.noColor))

	top, found, err := git.Repo{Runner: p.loggedGit(logger)}.TopLevel(ctx)
	if err != nil {
		broke := initBroke(req, result.Say("git could not find the top level of the repository: %s", err.Error()))

		return "", &broke
	}

	if !found {
		return "", req.Refused(result.Say("%s installs in a repository, and the working directory is in none", flagProject),
			result.NextStep("run it again from inside a repository, or without %s", flagProject))
	}

	return filepath.Join(top, claudeDir), nil
}

// initBroke returns the broke result of req for reason, with fixing what it names and running the
// same words again as the next step.
func initBroke(req landing.Request, reason result.Phrase) result.Result {
	return *req.Broke(reason,
		result.NextStep("fix what the reason names, then run %s again", result.Command(strings.Join(req.Words, " "))))
}

// installBroke returns the broke result of req for err, the failure of a step of the install.
func installBroke(req landing.Request, err error) result.Result {
	return initBroke(req, result.Say("the install broke: %s", err.Error()))
}

// installSkills writes each skill of shipped, a directory of its own at its root, to the directory
// of the same name under dir, creating the directories it needs, and returns one line per skill it
// installed, updated, or left as it is because its directory is a link. It stops at the first
// write that fails, with an error that names the path.
func installSkills(shipped fs.FS, dir string) ([]result.Phrase, error) {
	entries, err := fs.ReadDir(shipped, ".")
	if err != nil {
		return nil, fmt.Errorf("read the shipped skills: %w", err)
	}

	var lines []result.Phrase

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		line, err := installSkill(shipped, entry.Name(), filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}

		if line != nil {
			lines = append(lines, *line)
		}
	}

	return lines, nil
}

// installSkill writes every file of the skill name in shipped to dest, and returns the line that
// says it installed or updated the skill, or nil when every file was already in place. A dest
// that is a link is left as it is, and the line names its target.
func installSkill(shipped fs.FS, name, dest string) (*result.Phrase, error) {
	info, err := os.Lstat(dest)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", dest, err)
	}

	if existed && info.Mode()&fs.ModeSymlink != 0 {
		target, linkErr := os.Readlink(dest)
		if linkErr != nil {
			return nil, fmt.Errorf("read the link %s: %w", dest, linkErr)
		}

		line := result.Say("Left the %s skill at %s as it is: it is a link to %s", name, dest, target)

		return &line, nil
	}

	changed := false
	err = fs.WalkDir(shipped, name, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		wrote, err := placeFile(shipped, file, filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(file, name+"/"))))
		changed = changed || wrote

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("install the %s skill: %w", name, err)
	}

	switch {
	case !changed:
		return nil, nil
	case existed:
		line := result.Say("Updated the %s skill at %s", name, dest)

		return &line, nil
	default:
		line := result.Say("Installed the %s skill at %s", name, dest)

		return &line, nil
	}
}

// placeFile writes the shipped file to dest, with its parent directories, unless dest already
// holds the same content, and reports whether it wrote. hookScript is written executable.
func placeFile(shipped fs.FS, file, dest string) (bool, error) {
	want, err := fs.ReadFile(shipped, file)
	if err != nil {
		return false, fmt.Errorf("read the shipped %s: %w", file, err)
	}

	// A dest that cannot be read is written over, and the write says what is wrong with it.
	//nolint:gosec // G304: dest is the path of a shipped file under the Claude Code directory.
	if have, err := os.ReadFile(dest); err == nil && bytes.Equal(have, want) {
		return false, nil
	}

	mode := os.FileMode(fileMode)
	if path.Base(file) == hookScript {
		mode = scriptMode
	}

	if err := os.MkdirAll(filepath.Dir(dest), dirMode); err != nil {
		return false, fmt.Errorf("create the directory of %s: %w", dest, err)
	}

	if err := os.WriteFile(dest, want, mode); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}

	// WriteFile keeps the mode of a file that exists, and the umask trims the mode of a new one.
	if err := os.Chmod(dest, mode); err != nil {
		return false, fmt.Errorf("set the mode of %s: %w", dest, err)
	}

	return true, nil
}
