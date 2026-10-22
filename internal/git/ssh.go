package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jeffzi/ils/internal/child"
)

const (
	sshProgram = "ssh"
	// sshInstallURL is the home of OpenSSH, whose client ships with every operating system ils runs on.
	sshInstallURL = "https://www.openssh.com"
	// hostnameKey leads the line of `ssh -G` that names the host a connection goes to.
	hostnameKey = "hostname "
	// gitHubHost is the host gh files every host under it as.
	gitHubHost = "github.com"

	// sshAlias names the one-shot git alias that runs a configured ssh command line through the
	// shell git itself uses. Git runs a program git-<name> found on PATH before an alias of that
	// name, so the name is one no user has a program for.
	sshAlias = "ils-read-ssh-host"
	// printConfigFlag makes ssh print the config it would connect with, the output hostnameIn reads.
	printConfigFlag = "-G"
	// shellNotFoundCode is the exit of a shell for a command it cannot find.
	shellNotFoundCode = 127
	// gitCannotRunCode is the exit of git when it cannot start the program of a one-word command
	// line, which it runs without a shell. It is also git's exit for any fatal error.
	gitCannotRunCode = 128
)

// plainHostname returns the host plain ssh connects to when asked for host, the ssh git runs when
// no ssh command is configured, read from `ssh -G`. Like the ssh git would run, it is found on the
// PATH of the runner's environment and runs with that environment, from the runner's directory.
//
// An ssh missing from that PATH is a *child.ToolMissingError and a non-zero exit is a
// *child.ExitError.
func (r Repo) plainHostname(ctx context.Context, host string) (string, error) {
	// The host comes from a URL: after "--", one that starts with a dash is never read as an option.
	args := []string{printConfigFlag, "--", host}

	result, err := invocation{
		program: sshProgram, installURL: sshInstallURL, args: args,
		dir: r.Runner.Dir, env: r.Runner.Env, onEnvPath: true,
	}.run(ctx)
	if err != nil {
		return "", err
	}

	return hostnameIn(result.Stdout, append([]string{sshProgram}, args...))
}

// hostnameIn returns the host the `ssh -G` output stdout names, read as foldGitHubHost reads it.
// Output with no hostname line is an error naming argv, the call that printed it, joined by spaces.
func hostnameIn(stdout string, argv []string) (string, error) {
	for line := range strings.SplitSeq(stdout, "\n") {
		if name, found := strings.CutPrefix(line, hostnameKey); found {
			return foldGitHubHost(strings.TrimSpace(name)), nil
		}
	}

	return "", fmt.Errorf("%s printed no hostname", child.CommandLine(argv))
}

// foldGitHubHost returns github.com for any host under it, such as ssh.github.com, matched without
// regard to case. The dot in the suffix keeps lookalikes such as fake-github.com as they are.
func foldGitHubHost(host string) string {
	if strings.HasSuffix(strings.ToLower(host), "."+gitHubHost) {
		return gitHubHost
	}

	return host
}

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
	if line, _ := r.Runner.getenv("GIT_SSH_COMMAND"); line != "" {
		return sshCommand{line: line, setting: "GIT_SSH_COMMAND"}, nil
	}

	line, _, err := r.config(ctx, "core.sshCommand")
	if err != nil {
		return sshCommand{}, err
	}

	if line != "" {
		return sshCommand{line: line, setting: "core.sshCommand"}, nil
	}

	if program, _ := r.Runner.getenv("GIT_SSH"); program != "" {
		return sshCommand{program: program, setting: "GIT_SSH"}, nil
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

	word, ok := command.program, true
	if command.line != "" {
		word, ok = firstWord(command.line)
	}

	if !ok {
		return variantAuto, nil
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

// firstWord returns the first word of line as git splits a command line, which lineWords
// describes, except that git reads a line that starts with a space as having an empty first word.
// It returns false when git cannot split line.
func firstWord(line string) (string, bool) {
	words, ok := lineWords(line)
	if !ok || len(words) == 0 || isSpace(line[0]) {
		return "", ok
	}

	return words[0], true
}

// programWord returns the program the shell runs for line: the first word after any leading
// NAME=value assignments and any leading exec, command or env prefix, where the NAME=value words
// right after env are also skipped. It returns false when line cannot be split or holds no such
// word, as when it is only assignments, and when a word starting with a dash follows a prefix:
// the prefix's options may take the next word as their value (`exec -a name ssh`), so the program
// cannot be told apart from them. A dash-led first word with no prefix before it is the program.
func programWord(line string) (string, bool) {
	words, ok := lineWords(line)
	if !ok {
		return "", false
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
	if !found || name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}

	return !strings.ContainsFunc(name, func(c rune) bool {
		return c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9')
	})
}

// lineWords returns the words of line as git splits a command line: spaces outside quotes separate
// words, single and double quotes group, a pair of quotes with nothing between them is an empty
// word, and a backslash outside single quotes takes the next character as it is. It returns false
// when git cannot split line: a quote left open or a trailing backslash.
func lineWords(line string) ([]string, bool) {
	var (
		words  []string
		word   []byte
		quote  byte
		inWord bool
	)

	for i := 0; i < len(line); i++ {
		c := line[i]

		switch {
		case quote == 0 && isSpace(c):
			if inWord {
				words = append(words, string(word))
				word, inWord = word[:0], false
			}

			continue
		case quote == 0 && (c == '\'' || c == '"'):
			quote, inWord = c, true

			continue
		case c == quote:
			quote = 0

			continue
		case c == '\\' && quote != '\'':
			i++
			if i == len(line) {
				return nil, false
			}

			c = line[i]
		}

		word, inWord = append(word, c), true
	}

	if inWord {
		words = append(words, string(word))
	}

	return words, quote == 0
}

// isSpace reports whether c is one of the ASCII spaces git splits a command line on.
func isSpace(c byte) bool {
	return strings.IndexByte(" \t\n\v\f\r", c) >= 0
}

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

	if variant == variantPlain {
		if missing := r.Runner.missingProgram(command); missing != nil {
			return "", missing
		}

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
		result, err = r.Runner.Run(ctx, append([]string{"-c", "alias." + sshAlias + "=!" + command.line, sshAlias}, args...)...)
	} else {
		result, err = invocation{
			program: command.program, args: args,
			dir: r.Runner.Dir, env: r.Runner.Env, onEnvPath: true,
		}.run(ctx)
	}

	if notFound, ok := errors.AsType[*child.ToolMissingError](err); ok && command.line == "" {
		return "", &SSHCommandMissingError{Setting: command.setting, Program: command.program, Err: notFound.Err}
	}

	argv := append([]string{named}, args...)
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

// missingProgram returns the *SSHCommandMissingError for the GIT_SSH program of command when it is
// not found on the PATH git searches; otherwise, and for a command line, it returns nil.
func (r Runner) missingProgram(command sshCommand) *SSHCommandMissingError {
	if command.program == "" {
		return nil
	}

	return r.notFound(command, command.program)
}

// missingLineProgram returns the *SSHCommandMissingError for the program of command's line, which
// programWord picks past leading assignments and exec, command or env, when git exited as it does
// for a program it cannot find and no program of that name is found on the PATH git and its shell
// search; otherwise, and for a line that names no program, it returns nil. A one-word line that git
// cannot start exits gitCannotRunCode and a longer one exits shellNotFoundCode from git's shell;
// both codes are also exits of a program that runs, which is why the lookup decides.
func (r Runner) missingLineProgram(command sshCommand, exitErr *child.ExitError) *SSHCommandMissingError {
	if command.line == "" || (exitErr.Code != shellNotFoundCode && exitErr.Code != gitCannotRunCode) {
		return nil
	}

	word, ok := programWord(command.line)
	if !ok {
		return nil
	}

	return r.notFound(command, word)
}

// notFound returns the *SSHCommandMissingError for program, a program of command, when it is not
// found on the PATH git searches; otherwise it returns nil.
func (r Runner) notFound(command sshCommand, program string) *SSHCommandMissingError {
	if _, err := r.lookPath(program); err != nil {
		return &SSHCommandMissingError{Setting: command.setting, Program: program, Err: err}
	}

	return nil
}

// config reads key from the repository's config. A key that is not set, which `git config --get`
// reports by exiting 1, is not an error: it returns an empty value and set false, so a caller can
// tell it from a failure of git itself.
func (r Repo) config(ctx context.Context, key string) (value string, set bool, err error) {
	return r.optionalLine(ctx, "config", "--get", key)
}
