package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeffzi/ils/internal/child"
)

const (
	sshProgram = "ssh"
	// sshInstallURL is the home of OpenSSH, whose client ships with every operating system ils runs on.
	sshInstallURL = "https://www.openssh.com"
	// hostnameKey leads the line of `ssh -G` that names the host a connection goes to.
	hostnameKey = "hostname "

	// sshAlias names the one-shot git alias that runs a configured ssh command line through the
	// shell git itself uses. Git runs a program git-<name> found on PATH before an alias of that
	// name, so the name is one no user has a program for.
	sshAlias = "ils-read-ssh-host"
	// printConfigFlag makes ssh print the config it would connect with, the output hostnameIn reads.
	printConfigFlag = "-G"
	// shellNotFoundCode is the exit of a shell for a command it cannot find.
	shellNotFoundCode = 127

	// gitSSHCommandVar, sshCommandKey and gitSSHVar are the three settings that configure the ssh
	// command git connects with, as SSHCommandMissingError.Setting reports them.
	gitSSHCommandVar = "GIT_SSH_COMMAND"
	sshCommandKey    = "core.sshCommand"
	gitSSHVar        = "GIT_SSH"
)

// GitHubHost is the host gh files every host under it as.
const GitHubHost = "github.com"

// ---------------------------------------------------------------------------
// Hostname reading
// ---------------------------------------------------------------------------

// plainHostname returns the host plain ssh connects to when asked for host, the ssh git runs when
// no ssh command is configured, read from `ssh -G`. Like the ssh git would run, it is found on the
// PATH of the runner's environment and runs with that environment, from the runner's directory.
//
// An ssh missing from that PATH is a *child.ToolMissingError and a non-zero exit is a
// *child.ExitError.
func (r Repo) plainHostname(ctx context.Context, host string) (string, error) {
	// The host comes from a URL: after "--", one that starts with a dash is never read as an option.
	args := []string{printConfigFlag, "--", host}

	result, err := r.Runner.childProgram(sshProgram, sshInstallURL, args, r.Runner.lookPath).Run(ctx)
	if err != nil {
		return "", err
	}

	return hostnameIn(result.Stdout, child.Argv(sshProgram, args))
}

// hostnameIn returns the host the `ssh -G` output stdout names, read as canonicalHost reads it.
// Output with no hostname line is an error naming argv, the call that printed it, joined by spaces.
func hostnameIn(stdout string, argv []string) (string, error) {
	for line := range strings.SplitSeq(stdout, "\n") {
		if name, found := strings.CutPrefix(line, hostnameKey); found {
			return canonicalHost(strings.TrimSpace(name)), nil
		}
	}

	return "", fmt.Errorf("%s printed no hostname", child.CommandLine(argv))
}

// canonicalHost returns host in lowercase, since a host name is the same in any case, and
// github.com for any host under it, such as ssh.github.com. The dot in the suffix keeps lookalikes
// such as fake-github.com as they are.
func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if strings.HasSuffix(host, "."+GitHubHost) {
		return GitHubHost
	}

	return host
}

// ---------------------------------------------------------------------------
// ssh variants and command lines
// ---------------------------------------------------------------------------

// sshVariant is the kind of ssh command git takes a configured command for, which decides the
// options git passes it.
type sshVariant int

const (
	// variantAuto is a command git does not recognize: git asks it `-G` and, when that fails,
	// passes it no options, as it does for variantPlain.
	variantAuto sshVariant = iota
	// variantSSH is OpenSSH, which answers `-G`.
	variantSSH
	// variantPlain is plink, putty, tortoiseplink, or simple: git never asks any of them `-G`.
	variantPlain
)

// SSHCommandMissingError is an ssh command a user configured git to connect with whose program is
// not found, a setting to fix rather than a tool to install: a GIT_SSH program of any variant, or a
// command line of variant ssh or auto. A command line of variant plain is never checked.
type SSHCommandMissingError struct {
	// Err is the error the lookup of Program failed with, which Unwrap returns.
	Err error
	// Setting is where the command is configured: GIT_SSH_COMMAND, core.sshCommand or GIT_SSH.
	Setting string
	// Program is the program the command line runs, past any leading assignments and exec, command
	// or env, or the program GIT_SSH names.
	Program string
}

// Error names the program, the setting that configures it, and that it is not found.
func (e *SSHCommandMissingError) Error() string {
	return fmt.Sprintf("%s, the ssh command %s names, is not found", e.Program, e.Setting)
}

// Unwrap returns why the program was not found.
func (e *SSHCommandMissingError) Unwrap() error {
	return e.Err
}

// sshCommand is the ssh command a user configured git to connect with. At most one of line and
// program is set; when neither is, git connects with plain ssh and the command is the zero value.
type sshCommand struct {
	// line is a command line from GIT_SSH_COMMAND or core.sshCommand, which git hands to its shell.
	line string
	// program is the program GIT_SSH names, which git runs directly.
	program string
	// setting is GIT_SSH_COMMAND, core.sshCommand or GIT_SSH, as SSHCommandMissingError.Setting
	// reports it.
	setting string
}

// configuredSSH returns the ssh command git connects with, in git's order of precedence:
// GIT_SSH_COMMAND, then core.sshCommand, then GIT_SSH. An empty value counts as unset.
func (r Repo) configuredSSH(ctx context.Context) (sshCommand, error) {
	if line, _ := r.Runner.getenv(gitSSHCommandVar); line != "" {
		return sshCommand{line: line, setting: gitSSHCommandVar}, nil
	}

	line, _, err := r.config(ctx, sshCommandKey)
	if err != nil {
		return sshCommand{}, err
	}

	if line != "" {
		return sshCommand{line: line, setting: sshCommandKey}, nil
	}

	if program, _ := r.Runner.getenv(gitSSHVar); program != "" {
		return sshCommand{program: program, setting: gitSSHVar}, nil
	}

	return sshCommand{}, nil
}

// variant returns the variant git takes command for: GIT_SSH_VARIANT, else ssh.variant, else
// the basename of the command's first word, as `git help config` describes ssh.variant.
func (r Repo) variant(ctx context.Context, command sshCommand) (sshVariant, error) {
	value, set := r.Runner.getenv("GIT_SSH_VARIANT")
	if !set {
		var err error

		value, set, err = r.config(ctx, "ssh.variant")
		if err != nil {
			return variantAuto, err
		}
	}

	if set {
		if chosen := namedVariant(value); chosen != variantAuto {
			return chosen, nil
		}
	}

	word := command.program
	if command.line != "" {
		words, ok := SplitCommandLine(command.line)
		if !ok {
			return variantAuto, nil
		}

		word = words[0]
	}

	return basenameVariant(word), nil
}

// namedVariant returns the variant a value of ssh.variant or GIT_SSH_VARIANT names. Git reads any
// value it does not know as ssh.
func namedVariant(value string) sshVariant {
	switch value {
	case "auto":
		return variantAuto
	case "plink", "putty", "tortoiseplink", "simple":
		return variantPlain
	default:
		return variantSSH
	}
}

// basenameVariant returns the variant git takes a command for from the basename of its program,
// matched without regard to case.
func basenameVariant(program string) sshVariant {
	switch strings.ToLower(filepath.Base(program)) {
	case "ssh", "ssh.exe":
		return variantSSH
	case "plink", "plink.exe", "tortoiseplink", "tortoiseplink.exe":
		return variantPlain
	default:
		return variantAuto
	}
}

// programWord returns the program the shell runs for line: the first word after any leading
// NAME=value assignments and any leading exec, command or env prefix, where the NAME=value words
// right after env are also skipped. It returns false when line cannot be split or holds no such
// word, as when it is only assignments, and when a word starting with a dash follows a prefix:
// the prefix's options may take the next word as their value (`exec -a name ssh`), so the program
// cannot be told apart from them. A dash-led first word with no prefix before it is the program.
func programWord(line string) (string, bool) {
	// The shell drops the empty words git keeps where whitespace starts or ends a line. Leading
	// whitespace can be neither quoted nor escaped, so trimming it leaves the other words as they
	// were; an empty last word after a whitespace character can only come from that whitespace.
	line = strings.TrimLeft(line, commandLineSpaces)
	if line == "" {
		return "", false
	}

	words, ok := SplitCommandLine(line)
	if !ok {
		return "", false
	}

	if last := len(words) - 1; words[last] == "" && isSpace(line[len(line)-1]) {
		words = words[:last]
	}

	assignments, prefixed := true, false

	for _, word := range words {
		switch {
		case assignments && isAssignment(word):
		case word == "exec" || word == "command":
			assignments, prefixed = false, true
		case word == "env":
			assignments, prefixed = true, true
		case prefixed && strings.HasPrefix(word, "-"):
			return "", false
		default:
			return word, true
		}
	}

	return "", false
}

// isAssignment reports whether word is a NAME=value assignment to the shell: a name of ASCII
// letters, digits and underscores, not starting with a digit, then an equals sign.
func isAssignment(word string) bool {
	name, _, found := strings.Cut(word, "=")

	return found && IsName(name)
}

// IsName reports whether s is a name to the shell: ASCII letters, digits and underscores, not
// starting with a digit, and not empty.
func IsName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}

	return !strings.ContainsFunc(s, func(c rune) bool { return !isNameChar(c) })
}

func isNameChar(c rune) bool {
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

// commandLineSpaces are the whitespace characters git splits a command line at; vertical tab and
// form feed are not among them.
const commandLineSpaces = " \t\n\r"

// SplitCommandLine returns the words of line as git splits a command line, such as an alias or
// GIT_SSH_COMMAND: each run of space, tab, newline or carriage return outside quotes separates two
// words, so a line that starts or ends with one has an empty first or last word. What single quotes
// hold is taken as it is, double quotes group, and a backslash anywhere else stands for the
// character after it. It returns at least one word, and false where git refuses line: a quote left
// open, or a backslash at the end.
func SplitCommandLine(line string) ([]string, bool) {
	var words []string
	var word strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == 0 && isSpace(c):
			words = append(words, word.String())
			word.Reset()
			for i+1 < len(line) && isSpace(line[i+1]) {
				i++
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		case c == '\\' && quote != '\'':
			i++
			if i == len(line) {
				return nil, false
			}

			word.WriteByte(line[i])
		default:
			word.WriteByte(c)
		}
	}

	if quote != 0 {
		return nil, false
	}

	return append(words, word.String()), true
}

func isSpace(c byte) bool {
	return strings.IndexByte(commandLineSpaces, c) >= 0
}

// ---------------------------------------------------------------------------
// Configured ssh command hostname
// ---------------------------------------------------------------------------

// sshHostname returns the host the ssh command git connects with reaches when asked for host, read
// with `-G` as the command's variant allows and with plain `ssh -G` otherwise.
//
// A GIT_SSH program that is not found is an *SSHCommandMissingError, whatever its variant: git runs
// it directly, so its lookup is exact. A command line of variant ssh or auto whose program is not
// found is one too. A command line of variant plain is not checked: ils never runs it, and its
// first word may be a shell builtin or an assignment, so plain ssh reads the host. A command of
// variant ssh that exits non-zero is a *child.ExitError naming the command as configured. A command
// of variant auto that exits non-zero is read as git reads it, as variant simple.
func (r Repo) sshHostname(ctx context.Context, host string) (string, error) {
	command, err := r.configuredSSH(ctx)
	if err != nil {
		return "", err
	}

	if command == (sshCommand{}) {
		return r.plainHostname(ctx, host)
	}

	variant, err := r.variant(ctx, command)
	if err != nil {
		return "", err
	}

	// A GIT_SSH program that is not on the PATH git searches is named; a command line is not.
	if variant == variantPlain && command.program != "" {
		if missing := r.Runner.missingOn(command, command.program); missing != nil {
			return "", missing
		}
	}

	if variant == variantPlain {
		return r.plainHostname(ctx, host)
	}

	name, err := r.askSSH(ctx, command, host)
	if _, refused := errors.AsType[*child.ExitError](err); refused && variant == variantAuto {
		return r.plainHostname(ctx, host)
	}

	return name, err
}

// askSSH runs command with `-G host` and returns the host its output names. A command line runs
// as a one-shot shell alias, from the top of the working tree, as git runs it: directly when it is
// one word, through git's shell otherwise. A program runs directly, found on the PATH of the
// runner's environment and run with that environment, from the runner's directory.
//
// A program that is not found, or a command line whose program is not found, is an
// *SSHCommandMissingError. Any other non-zero exit is a *child.ExitError whose argv starts with the
// command as configured.
func (r Repo) askSSH(ctx context.Context, command sshCommand, host string) (string, error) {
	var (
		result Result
		err    error
	)

	args := []string{printConfigFlag, host}

	named := command.program
	if command.line != "" {
		named = command.line
		aliasArgs := append([]string{"-c", "alias." + sshAlias + "=!" + command.line, sshAlias}, args...)
		result, err = r.Runner.Run(ctx, aliasArgs...)
	} else {
		result, err = r.Runner.childProgram(command.program, "", args, r.Runner.lookPath).Run(ctx)
	}

	if notFound, ok := errors.AsType[*child.ToolMissingError](err); ok && command.line == "" {
		return "", &SSHCommandMissingError{Setting: command.setting, Program: command.program, Err: notFound.Err}
	}

	argv := child.Argv(named, args)
	if exitErr, ok := errors.AsType[*child.ExitError](err); ok {
		if missing := r.Runner.missingLineProgram(command, exitErr); missing != nil {
			return "", missing
		}

		return "", &child.ExitError{Argv: argv, Code: exitErr.Code, Stderr: exitErr.Stderr}
	}

	if err != nil {
		return "", err
	}

	return hostnameIn(result.Stdout, argv)
}

// ---------------------------------------------------------------------------
// Environment lookup
// ---------------------------------------------------------------------------

// getenv returns the value key has in the environment git runs with, and whether it is set, as
// EnvValue reads it.
func (r Runner) getenv(key string) (string, bool) {
	return EnvValue(r.Environ(), key)
}

// WithoutKeys returns env, a list of key=value entries, without the entries whose key is one of
// keys, matched exactly. It reuses env's storage, as slices.DeleteFunc does.
func WithoutKeys(env []string, keys ...string) []string {
	return slices.DeleteFunc(env, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")

		return slices.Contains(keys, name)
	})
}

// EnvValue returns the value key has in env, a list of key=value entries, and whether env sets it.
// A key set twice has its last value, as it does for a child started with env, and keys match
// exactly.
func EnvValue(env []string, key string) (string, bool) {
	var (
		value string
		set   bool
	)

	for _, entry := range env {
		name, entryValue, ok := strings.Cut(entry, "=")
		if ok && name == key {
			value, set = entryValue, true
		}
	}

	return value, set
}

// lookPath finds name as exec.LookPath does, on the PATH of the environment git runs with: the
// PATH on which git, and the shell it starts, find a program. With no environment of its own, that
// is this process's PATH.
//
// A name that holds a path separator is not searched for. Each absolute directory of PATH is tried
// as exec.LookPath tries a path; a relative one is skipped, as exec.LookPath refuses a program it
// finds there.
func (r Runner) lookPath(name string) (string, error) {
	if r.Env == nil || filepath.Base(name) != name {
		return exec.LookPath(name)
	}

	path, _ := r.getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}

		if found, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return found, nil
		}
	}

	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// missingLineProgram returns the *SSHCommandMissingError for the program of command's line, which
// programWord picks past leading assignments and exec, command or env, when git exited as it does
// for a program it cannot find and no program of that name is found on the PATH git and its shell
// search; otherwise, and for a line that names no program, it returns nil. A one-word line that git
// cannot start exits fatalCode and a longer one exits shellNotFoundCode from git's shell;
// both codes are also exits of a program that runs, which is why the lookup decides.
func (r Runner) missingLineProgram(command sshCommand, exitErr *child.ExitError) *SSHCommandMissingError {
	if command.line == "" || (exitErr.Code != shellNotFoundCode && exitErr.Code != fatalCode) {
		return nil
	}

	word, ok := programWord(command.line)
	if !ok {
		return nil
	}

	return r.missingOn(command, word)
}

// missingOn returns the *SSHCommandMissingError for program, a program of command, when it is not
// found on the PATH git searches; otherwise it returns nil.
func (r Runner) missingOn(command sshCommand, program string) *SSHCommandMissingError {
	if _, err := r.lookPath(program); err != nil {
		return &SSHCommandMissingError{Setting: command.setting, Program: program, Err: err}
	}

	return nil
}
