// Package commandline names the command words and root flags that the command line registers and
// the guard reads, so that both agree on what ils runs.
package commandline

// ParseCommand, GuardCommand and HookCommand are the command words of `ils parse`, `ils guard` and
// `ils hook`, the commands that read a command and change nothing.
const (
	ParseCommand = "parse"
	GuardCommand = "guard"
	HookCommand  = "hook"
)

// FlagJSON, FlagDebug, FlagDebugShort, FlagNoColor, FlagHelp, FlagHelpShort and FlagVersion are the
// flags of the root command of ils that take no value, as the command line spells them. The guard
// reads them through RootSwitches.
const (
	FlagJSON       = "--json"
	FlagDebug      = "--debug"
	FlagDebugShort = "-d"
	FlagNoColor    = "--no-color"
	FlagHelp       = "--help"
	FlagHelpShort  = "-h"
	FlagVersion    = "--version"
)

// RootSwitches returns every flag of the root command of ils that takes no value.
func RootSwitches() []string {
	return []string{FlagJSON, FlagDebug, FlagDebugShort, FlagNoColor, FlagHelp, FlagHelpShort, FlagVersion}
}
