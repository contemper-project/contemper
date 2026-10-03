package main

import (
	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	var (
		to           string
		arch         string
		name         string
		volumes      []string
		serialLog    string
		expect       string
		timeoutStr   string
		quiet        bool
		verbose      bool
		progressMode string
	)

	cmd := &cobra.Command{
		Use:   "deploy [flags] <bundle-dir | group-file>",
		Short: "Deploy a contemper bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd.Context(), cmd, deployOptions{
				bundleDir:    args[0],
				arch:         arch,
				to:           to,
				name:         name,
				volumes:      volumes,
				serialLog:    serialLog,
				expect:       expect,
				timeout:      timeoutStr,
				quiet:        quiet,
				verbose:      verbose,
				progressMode: progressMode,
			})
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "deploy target (local-qemu)")
	cmd.Flags().StringVar(&arch, "arch", "", "architecture to boot from a group file (default: the host's); must match a bundle directory's")
	cmd.Flags().StringVar(&name, "name", "", "local-qemu instance name (default: the source image's repository name)")
	cmd.Flags().StringArrayVar(&volumes, "volume", nil, "set or override a volume's size: --volume <path>=<size>, repeatable")
	cmd.Flags().StringVar(&serialLog, "serial-log", "", "file to write the VM's serial console to")
	cmd.Flags().StringVar(&expect, "expect", "", "exit 0 once this string appears on the serial console")
	cmd.Flags().StringVar(&timeoutStr, "timeout", "60s", "timeout waiting for --expect")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show the qemu invocation")
	cmd.Flags().StringVar(&progressMode, "progress", "auto", "progress output: auto, tty, or plain")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}
