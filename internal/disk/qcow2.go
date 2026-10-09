package disk

import (
	"context"
	"fmt"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
)

// ConvertToQcow2 runs `qemu-img convert -O qcow2 rawPath qcow2Path`. If
// stage is live (see progress.Stage.Live) it runs with -p and reports
// qemu-img's percentage on stage as it goes; those readouts are left out
// of the error message if the conversion fails.
// Callers that want to skip qcow2 conversion and work with the raw image
// when qemu-img is missing should check hostenv.Find("qemu-img")
// themselves before calling this. Canceling ctx stops it.
func ConvertToQcow2(ctx context.Context, rawPath, qcow2Path string, rep *progress.Reporter, stage *progress.Stage) error {
	qemuImg, err := hostenv.Required("qemu-img")
	if err != nil {
		return err
	}
	args := []string{"convert", "-O", "qcow2", rawPath, qcow2Path}
	var onLine func(string)
	if stage.Live() {
		args = []string{"convert", "-p", "-O", "qcow2", rawPath, qcow2Path}
		onLine = func(line string) {
			if pct, ok := parseQemuProgress(line); ok {
				stage.SetProgressPercent(pct)
			}
		}
	}
	rep.VerboseCmd(qemuImg, args)
	out, err := runCmdStream(ctx, "", qemuImg, onLine, args...)
	if err != nil {
		return fmt.Errorf("qemu-img convert: %w\n%s", err, stripQemuProgress(out))
	}
	return nil
}
