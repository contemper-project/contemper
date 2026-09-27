package disk

import (
	"fmt"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/progress"
)

// ConvertToQcow2 runs `qemu-img convert -O qcow2 rawPath qcow2Path`.
// Callers that want to skip qcow2 conversion and work with the raw image
// when qemu-img is missing should check hostenv.Find("qemu-img")
// themselves before calling this.
func ConvertToQcow2(rawPath, qcow2Path string, rep *progress.Reporter) error {
	qemuImg, err := hostenv.Required("qemu-img")
	if err != nil {
		return err
	}
	args := []string{"convert", "-O", "qcow2", rawPath, qcow2Path}
	rep.VerboseCmd(qemuImg, args)
	out, err := runCmd("", qemuImg, args...)
	if err != nil {
		return fmt.Errorf("qemu-img convert: %w\n%s", err, out)
	}
	return nil
}
