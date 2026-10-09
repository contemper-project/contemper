package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBuildCmd() *cobra.Command {
	var (
		file      string
		tag       string
		buildArgs []string
		imageOnly bool
		engine    string
	)
	convOpts := &convertOptions{}

	cmd := &cobra.Command{
		Use:   "build [flags] [context]",
		Short: "Build an image with docker buildx or podman and convert it into a bootable VM disk bundle",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buildContext := "."
			if len(args) == 1 {
				buildContext = args[0]
			}
			if !imageOnly && convOpts.target == "" {
				return fmt.Errorf("--target is required unless --image-only is given")
			}
			return runBuild(cmd.Context(), cmd, buildOptions{
				context:   buildContext,
				file:      file,
				tag:       tag,
				buildArgs: buildArgs,
				imageOnly: imageOnly,
				engine:    engine,
				convert:   *convOpts,
			})
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "path to the Containerfile/Dockerfile (default: <context>/Dockerfile, or <context>/Containerfile when there is no Dockerfile)")
	cmd.Flags().StringVarP(&tag, "tag", "t", "", "image tag (default: derived from the context directory's name, tagged dev)")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "set a build-time variable: --build-arg KEY=VALUE, repeatable")
	cmd.Flags().BoolVar(&imageOnly, "image-only", false, "stop after building the image and loading it into the engine's image store; print the image reference instead of converting it")

	cmd.Flags().StringVar(&engine, "engine", "auto", "build engine: auto (docker buildx if usable, else podman), docker or podman")

	// The same --arch flag drives both the build --platform and the
	// conversion; registerConvertFlags defines it once, on convOpts.arch.
	registerConvertFlags(cmd, convOpts)

	return cmd
}
