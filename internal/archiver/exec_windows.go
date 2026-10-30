//go:build windows

package archiver

import (
	"os/exec"
	"syscall"
)

func prepareExec(cmd *exec.Cmd) {
	// Prevent a console window from flashing while 7-Zip works.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
