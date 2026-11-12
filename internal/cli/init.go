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
	"runtime"
	"slices"
	"strings"

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
// in the Claude Code directory at user scope (see userDir), or with --project at the top level of
// the work tree the working directory lies in.
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
		"Set up .claude/ at the top level of the repository the working directory is in, not your own Claude Code directory")
	p.refuseFlags(c, extra)

	return c
}

// installClaude installs every skill of the app in the Claude Code directory installDir returns,
// creating it when it is missing, then adds the permission rules and, where the guard runs, its
// hook to the settings of that directory. The settings are read and checked before any skill is
// written, and written after. The result has one line per skill installed, updated, or left as it
// is because its directory is a link, and one per change to the settings; or one line saying
// everything was in place. Where the guard does not run, a line in the hook's place, before the
// rule lines, says where it does. A
// result of installDir ends the run; settings it cannot edit are a refusal; a failed read of the
// settings, or a failed write, ends the run broke.
func (p *parse) installClaude(ctx context.Context, project bool) result.Result {
	req := landing.Request{Command: initWord, Words: p.words, Args: []string{harnessClaude}, Project: project}

	dir, hook, refusal := p.installDir(ctx, req)
	if refusal != nil {
		return *refusal
	}

	guardRuns := guardSupported
	if p.app.guardRuns != nil {
		guardRuns = *p.app.guardRuns
	}

	if !guardRuns {
		hook = ""
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

	changed := len(lines) > 0 || settings.changed()
	for i, line := range settings.lines {
		if line.kind == stepChanged || i == 0 && !guardRuns {
			lines = append(lines, line.phrase)
		}
	}

	if !changed {
		lines = append(lines, result.Say("Everything was already in place."))
	}

	return result.Result{Command: req.Command, Words: req.Words, Lines: lines, Next: result.NextNone(), Outcome: result.Done}
}

// installDir returns the Claude Code directory the install of req writes to and the command of the
// guard's hook in it: the one at user scope (see userDir), or under --project the one at the top
// level of the work tree the working directory lies in. It returns the result that ends the run
// instead when userDir refuses, when a project run is outside a work tree, or when the read of the
// top level fails, which ends as a landing command's read made before any write does (see
// landing.Request.Unread).
func (p *parse) installDir(ctx context.Context, req landing.Request) (dir, hook string, refusal *result.Result) {
	if !req.Project {
		return userDir(req, p.app.Environ)
	}

	logger := p.logger(p.app.profile(p.app.StderrTerminal, p.noColor))

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
// directory, which USERPROFILE names on Windows and HOME elsewhere; the other variable is never
// read, so a HOME a Windows shell sets does not move the install. It returns the refusal of req
// instead when CLAUDE_CONFIG_DIR is not an absolute path or holds a character bash cannot be given,
// or when the home directory's variable is not set or empty.
func userDir(req landing.Request, environ []string) (dir, hook string, refusal *result.Result) {
	if config, _ := git.EnvValue(environ, claudeConfigDir); config != "" {
		if !filepath.IsAbs(config) {
			return "", "", req.Refused(result.Say("%s is %s, which is not an absolute path", claudeConfigDir, config),
				result.NextStep("set %s to an absolute path, or unset it", claudeConfigDir))
		}

		script, err := syntax.Quote(filepath.Join(config, filepath.FromSlash(guardScript)), syntax.LangBash)
		if err != nil {
			return "", "", req.Refused(result.Say("%s is %s, which no bash command can name: %s", claudeConfigDir, config, err.Error()),
				result.NextStep("set %s to another path, or unset it", claudeConfigDir))
		}

		return config, "bash " + script, nil
	}

	homeVariable := "HOME"
	if runtime.GOOS == "windows" {
		homeVariable = "USERPROFILE"
	}

	home, _ := git.EnvValue(environ, homeVariable)
	if home == "" {
		return "", "", req.Refused(result.Say("%s is not set, and the skills install under it", homeVariable),
			result.NextStep("set %s, or run %s in a repository", homeVariable, result.Command(initWord+" "+harnessClaude+" "+flagProject)))
	}

	return filepath.Join(home, claudeDir), userHook, nil
}

// installBroke returns the broke result of req for err, the failure of a step of the install, with
// fixing what the reason names and running the same words again as the next step.
func installBroke(req landing.Request, err error) result.Result {
	return *req.Broke(result.Say("the install broke: %s", err.Error()),
		result.NextStep("fix what the reason names, then run %s again", result.Command(strings.Join(req.Words, " "))))
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

		line := result.Say("Did not install the %s skill: %s is a link to %s, which Claude Code uses; remove the link to install the shipped skill",
			name, dest, target)

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
