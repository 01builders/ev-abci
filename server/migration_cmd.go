package server

import (
	"fmt"

	"github.com/spf13/cobra"
)

// MigrateToEvolveCmd returns a command to handle migration operations.
// This is a stub to enable ignite to scaffold this app correctly.
// ref: https://github.com/ignite/apps/blob/1ace9ddeace377072ddafd3a25024894447b17f2/evolve/template/constants.go#L8
func MigrateToEvolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migration utilities for validator unbonding",
		Long: `Migration utilities for the validator unbonding process.

The migration module handles gradual validator unbonding while staying on CometBFT.
No manual migration steps are required - migrations are triggered and executed
automatically via governance proposals.

This command exists for compatibility with the Ignite CLI scaffolding but does
not perform any actions, as the StayOnComet migration flow is fully automated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("No manual migration action required.")
			fmt.Println("Migrations happen automatically via governance proposals.")
			fmt.Println("The chain continues running on CometBFT throughout the migration process.")
			return nil
		},
	}

	return cmd
}
