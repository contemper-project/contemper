package main

import (
	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/volume"
)

func newConvertCmd() *cobra.Command {
	var (
		target       string
		supportRef   string
		outDir       string
		arch         string
		rootSizeStr  string
		noFstab      bool
		volumeHelper string
		noVolHelper  bool
		keepRaw      bool
		quiet        bool
		verbose      bool
		progressMode string
	)

	cmd := &cobra.Command{
		Use:   "convert [flags] <source-ref>",
		Short: "Convert a contemper-ready OCI image into a bootable VM disk bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConvert(cmd, convertOptions{
				sourceRef:    args[0],
				target:       target,
				supportRef:   supportRef,
				outDir:       outDir,
				arch:         arch,
				rootSize:     rootSizeStr,
				noFstab:      noFstab,
				volumeHelper: volumeHelper,
				noVolHelper:  noVolHelper,
				keepRaw:      keepRaw,
				quiet:        quiet,
				verbose:      verbose,
				progressMode: progressMode,
			})
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "conversion target (qemu, incus; or canonical qemu-qcow2, incus-qcow2)")
	cmd.Flags().StringVar(&supportRef, "support", "", "support image reference; replaces the target's default")
	cmd.Flags().StringVarP(&outDir, "out", "o", ".", "directory to write the bundle into, as a subdirectory named after the image (a previous bundle of that name is replaced)")
	cmd.Flags().StringVar(&arch, "arch", "", "target architecture (amd64|arm64); defaults to host")
	cmd.Flags().StringVar(&rootSizeStr, "root-size", "", "override the root partition size (e.g. 2GiB)")
	cmd.Flags().BoolVar(&noFstab, "no-fstab", false, "don't append fstab lines for declared volumes")
	cmd.Flags().StringVar(&volumeHelper, "volume-helper", "", "override the volume-formatting support image (default: "+volume.DefaultHelperRef+")")
	cmd.Flags().BoolVar(&noVolHelper, "no-volume-helper", false, "don't merge the volume-formatting helper even if volumes are declared")
	cmd.Flags().BoolVar(&keepRaw, "keep-raw", false, "keep the intermediate disk.raw file")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output (stdout still prints the bundle path)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show each host tool invocation")
	cmd.Flags().StringVar(&progressMode, "progress", "auto", "progress output: auto, tty, or plain")
	_ = cmd.MarkFlagRequired("target")

	return cmd
}
