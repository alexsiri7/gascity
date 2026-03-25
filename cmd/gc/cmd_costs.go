package main

import (
	"io"

	"github.com/spf13/cobra"
)

// newCostsCmd creates the "gc costs" command with its subcommands.
func newCostsCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "costs",
		Short: "Token usage and cost reporting",
		Long:  `Commands for viewing and recording agent token usage and costs.`,
	}
	cmd.AddCommand(newCostsRecordCmd(stdout, stderr))
	return cmd
}
