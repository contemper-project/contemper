package disk

import (
	"context"
	"fmt"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
)

// ConvertToQcow2 runs `qemu-img convert -O qcow2 rawPath qcow2Path`.
// Callers that want to skip qcow2 conversion and work with the raw image
// when qemu-img is missing should check hostenv.Find("qemu-img")
// themselves before calling this. Canceling ctx stops it.
func ConvertToQcow2(ctx context.Context, rawPath, qcow2Path string, rep *progress.Reporter) error {
	qemuImg, err := hostenv.Required("qemu-img")
	if err != nil {
		return err
	}
	args := []string{"convert", "-O", "qcow2", rawPath, qcow2Path}
	rep.VerboseCmd(qemuImg, args)
	out, err := runCmd(ctx, "", qemuImg, args...)
	if err != nil {
		return fmt.Errorf("qemu-img convert: %w\n%s", err, out)
	}
	return nil
}
