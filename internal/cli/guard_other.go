//go:build !unix

package cli

import (
	"github.com/spf13/cobra"

	"github.com/jeffzi/ils/internal/result"
)

// parseCommand returns `ils parse`, which refuses every run: the guard runs on macOS and Linux.
func (p *parse) parseCommand() *cobra.Command {
	return p.unsupportedCommand(&cobra.Command{
		Use:     parseUse,
		Short:   parseShort,
		Long:    parseLong,
		Example: parseExamples,
	})
}

// guardCommand returns `ils guard`, which refuses every run: the guard runs on macOS and Linux.
func (p *parse) guardCommand() *cobra.Command {
	return p.unsupportedCommand(&cobra.Command{
		Use:     guardUse,
		Short:   guardShort,
		Long:    guardLong,
		Example: guardExamples,
	})
}

// unsupportedCommand completes c as a command that reads no flag and refuses every run.
func (p *parse) unsupportedCommand(c *cobra.Command) *cobra.Command {
	c.DisableFlagParsing = true
	c.Args = func(*cobra.Command, []string) error {
		return p.refuseWith(result.Say("the guard runs on macOS and Linux"), result.NextNone())
	}
	c.RunE = func(*cobra.Command, []string) error { return nil }

	return c
}
