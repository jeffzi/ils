//go:build unix

package guard_test

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"

	"github.com/jeffzi/ils/internal/guard"
)

func TestParse_WhenAllexportIsOn_MarksAGitVariableABuiltinAssigns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "read", command: "set -a; read -r GIT_CONFIG_PARAMETERS <<< x; git commit -m x"},
		{name: "printf -v", command: "set -a; printf -v GIT_CONFIG_PARAMETERS %s x; git status"},
		{name: "assign-default expansion", command: `set -a; echo "${GIT_CONFIG_PARAMETERS:=x}"; git status`},
		{name: "allexport turned on in an if body", command: "if true; then set -a; fi; read GIT_CONFIG_PARAMETERS; git status"},
		{name: "assign-default expansion in [[ ]]", command: "set -a; [[ -n ${GIT_CONFIG_PARAMETERS:=x} ]]; git status"},
		{name: "assign-default expansion in a case word", command: "set -a; case ${GIT_CONFIG_PARAMETERS:=x} in *) ;; esac; git status"},
		{name: "assign-default expansion in a for word list", command: "set -a; for v in ${GIT_CONFIG_PARAMETERS:=x}; do :; done; git status"},
		{name: "assign-default expansion in a declare argument", command: `set -a; declare v="${GIT_CONFIG_PARAMETERS:=x}"; git status`},
		{name: "read in a function defined before set -a and called after", command: "f() { read -r GIT_CONFIG_PARAMETERS; }; set -a; f; git status"},
		{name: "allexport turned on by eval", command: "eval 'set -a'; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "allexport turned on by a function call", command: "f() { set -a; }; f; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read in a bash -a string", command: "bash -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -o allexport string", command: "bash -o allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -a string after a one-dash long option", command: "bash -norc -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash -a string after a one-dash long option taking a value", command: "bash -rcfile x -a -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "printf -v in a string of bash found by the equals lookup", command: `=bash -a -c 'printf -v GIT_CONFIG_PARAMETERS %s "core.hooksPath=/dev/null"; git commit -m x'`},
		{name: "read in a zsh string under a long option spelled with capitals and underscores", command: "zsh --All_Export -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh -o string spelled with capitals and underscores", command: "zsh -o ALL_EXPORT -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh string where --allexport follows --no-allexport", command: "zsh --no-allexport --allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh string after set -o spelled with capitals and underscores", command: "zsh -c 'set -o All_Export; read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read after set -o spelled with capitals and underscores in an unknown shell", command: "set -o All_Export; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after setopt allexport", command: "setopt allexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after setopt of a non-literal option", command: `setopt "$o"; read -r GIT_CONFIG_PARAMETERS; git status`},
		{name: "read after setopt whose +o takes an attached name", command: "setopt +oVERBOSE allexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after unsetopt whose -o takes an attached name", command: "unsetopt -oNO_ALL_EXPORT; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after setopt where +a follows a name", command: "setopt allexport; setopt verbose +a; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after set +o with an attached name in an unknown shell", command: "set +oVERBOSE -a; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after set +o noallexport in an unknown shell", command: "set +o noallexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after set +o with a spelling only zsh reads, in an unknown shell", command: "set -a; set +o All_Export; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after set -o with an attached name bash reads as letters, in an unknown shell", command: "set -oxa; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "for loop name over a literal", command: "set -a; for GIT_CONFIG_PARAMETERS in x; do :; done; git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			want := guard.Mark{Kind: guard.GitVariable, Name: "GIT_CONFIG_PARAMETERS"}
			assert.Check(t, is.Contains(marksOf(got), want), "Parse(%q) marks", tt.command)
		})
	}
}

func TestParse_WhenAllexportIsOff_MarksNoGitVariableABuiltinAssigns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "turned on in a subshell", command: "(set -a); read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "turned on for a bash -a string only", command: "bash -a -c true; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read in a bash string after set -o spelled with capitals", command: "bash -c 'set -o ALLEXPORT; read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read after setopt noallexport", command: "setopt noallexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after unsetopt allexport", command: "unsetopt allexport; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read after set +a undoes set -a", command: "set -a; set +a; read -r GIT_CONFIG_PARAMETERS; git status"},
		{name: "read in a zsh string after set -o noallexport undoes set -a", command: "zsh -c 'set -a; set -o noallexport; read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh -a string after --no-allexport with dashes", command: "zsh -a --no-allexport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a zsh -a string after --noallexport spelled with capitals and underscores", command: "zsh -a --No_AllExport -c 'read -r GIT_CONFIG_PARAMETERS; git status'"},
		{name: "read in a bash string after set +o with an attached name", command: "bash -c 'set +oVERBOSE -a; read -r GIT_CONFIG_PARAMETERS; git status'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parse(t, tt.command)

			assert.Assert(t, len(got) > 0, "Parse(%q) = %v", tt.command, texts(got))
			assert.Check(t, is.Len(marksOf(got), 0), "Parse(%q) marks", tt.command)
		})
	}
}
