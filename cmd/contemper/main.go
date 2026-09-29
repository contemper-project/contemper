// Command contemper converts a contemper-ready OCI image into a bootable
// VM disk bundle, and can deploy that bundle locally for testing.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/buildinfo"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "contemper:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "contemper",
		Short:         "Convert OCI images into bootable VM disk bundles",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newBuildCmd())
	root.AddCommand(newConvertCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newGenDocsCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the contemper version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Get().String())
			return nil
		},
	}
}
