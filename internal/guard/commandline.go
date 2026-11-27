package guard

// ParseCommand, Command and HookCommand are the command words of `ils parse`, `ils guard` and
// `ils hook`, the commands that read a command and change nothing.
const (
	ParseCommand = "parse"
	Command      = "guard"
	HookCommand  = "hook"
)

// FlagJSON, FlagDebug, FlagDebugShort, FlagNoColor, FlagHelp, FlagHelpShort and FlagVersion are the
// flags of the root command of ils that take no value, as the command line spells them. The guard
// reads them through ilsSwitches.
const (
	FlagJSON       = "--json"
	FlagDebug      = "--debug"
	FlagDebugShort = "-d"
	FlagNoColor    = "--no-color"
	FlagHelp       = "--help"
	FlagHelpShort  = "-h"
	FlagVersion    = "--version"
)

// ilsSwitches are the flags the root command of ils knows and that take no value. A test of
// package cli fails when the root gains a switch they lack.
func ilsSwitches() map[string]bool {
	return setOf(FlagJSON, FlagDebug, FlagDebugShort, FlagNoColor, FlagHelp, FlagHelpShort, FlagVersion)
}
