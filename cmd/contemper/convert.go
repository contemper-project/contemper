package main

import (
	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/volume"
)

func newConvertCmd() *cobra.Command {
	opts := &convertOptions{}

	cmd := &cobra.Command{
		Use:   "convert [flags] <source-ref>",
		Short: "Convert a contemper-ready OCI image into a bootable VM disk bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.sourceRef = args[0]
			return runConvert(cmd.Context(), cmd, *opts)
		},
	}

	registerConvertFlags(cmd, opts)
	_ = cmd.MarkFlagRequired("target")

	return cmd
}

// registerConvertFlags adds every flag that governs the source-image ->
// bundle conversion step to cmd, binding them into opts. `convert` and
// `build` both call this, so the two commands' shared flags (everything
// but `build`'s own -t/-f/--build-arg/--image-only) stay defined in one
// place. `convert` makes --target required via MarkFlagRequired right
// after calling this; `build` only requires it when it isn't stopping at
// --image-only, so it checks that itself instead.
func registerConvertFlags(cmd *cobra.Command, opts *convertOptions) {
	cmd.Flags().StringVar(&opts.target, "target", "", "conversion target (qemu, incus; or canonical qemu-qcow2, incus-qcow2)")
	cmd.Flags().StringVar(&opts.supportRef, "support", "", "support image reference; replaces the target's default")
	cmd.Flags().StringVarP(&opts.outDir, "out", "o", ".", "directory to write the bundle into, as a subdirectory named after the image (a previous bundle of that name is replaced)")
	cmd.Flags().StringVar(&opts.arch, "arch", "", "target architecture: amd64, arm64, a comma-separated list of them, or all (every architecture the source provides; convert only); defaults to host")
	cmd.Flags().StringVar(&opts.rootSize, "root-size", "", "override the root partition size (e.g. 2GiB)")
	cmd.Flags().BoolVar(&opts.noFstab, "no-fstab", false, "don't append fstab lines for declared volumes or the ESP")
	cmd.Flags().StringVar(&opts.volumeHelper, "volume-helper", "", "override the volume-formatting support image (default: "+volume.DefaultHelperRef+")")
	cmd.Flags().BoolVar(&opts.noVolHelper, "no-volume-helper", false, "don't merge the volume-formatting helper even if volumes are declared")
	cmd.Flags().BoolVar(&opts.keepRaw, "keep-raw", false, "keep the intermediate disk.raw file")
	cmd.Flags().BoolVarP(&opts.quiet, "quiet", "q", false, "suppress progress output (stdout still prints the bundle path)")
	cmd.Flags().BoolVarP(&opts.verbose, "verbose", "v", false, "show each host tool invocation")
	cmd.Flags().StringVar(&opts.progressMode, "progress", "auto", "progress output: auto, tty, or plain")
}
