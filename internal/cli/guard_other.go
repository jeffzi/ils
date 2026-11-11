//go:build !unix

package cli

import (
	"github.com/spf13/cobra"

	"github.com/jeffzi/ils/internal/result"
)

// guardSupported reports false: outside Unix, the guard refuses every run.
const guardSupported = false

// parseCommand returns `ils parse`, which refuses every run: the guard runs on macOS and Linux.
func (p *parse) parseCommand() *cobra.Command {
	return p.unsupportedCommand(parseHeader())
}

// guardCommand returns `ils guard`, which refuses every run: the guard runs on macOS and Linux.
func (p *parse) guardCommand() *cobra.Command {
	return p.unsupportedCommand(guardHeader())
}

// hookCommand returns `ils hook`, which refuses every run with exit 2 and the reason on Stderr,
// where a harness reads a block: the guard runs on macOS and Linux.
func (p *parse) hookCommand() *cobra.Command {
	c := hookHeader()
	c.RunE = func(*cobra.Command, []string) error {
		p.quit(result.Refused.ExitCode(), guardUnsupported)

		return nil
	}

	return c
}

// unsupportedCommand completes c as a command that reads no flag and refuses every run.
func (p *parse) unsupportedCommand(c *cobra.Command) *cobra.Command {
	c.DisableFlagParsing = true
	c.Args = func(*cobra.Command, []string) error {
		return p.refuseWith(result.Say(guardUnsupported), result.NextNone())
	}
	c.RunE = func(*cobra.Command, []string) error { return nil }

	return c
}
