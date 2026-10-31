//go:build !unix

package cli

import (
	"github.com/spf13/cobra"

	"github.com/jeffzi/ils/internal/result"
)

// parseCommand returns `ils parse`, which refuses every run: the guard runs on macOS and Linux.
func (p *parse) parseCommand() *cobra.Command {
	return &cobra.Command{
		Use:                parseUse,
		Short:              parseShort,
		Long:               parseLong,
		Example:            parseExamples,
		DisableFlagParsing: true,
		Args: func(*cobra.Command, []string) error {
			return p.refuseWith(result.Say("the guard runs on macOS and Linux"), result.NextNone())
		},
		RunE: func(*cobra.Command, []string) error { return nil },
	}
}
