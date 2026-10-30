//go:build !windows

package archiver

import "os/exec"

func prepareExec(_ *exec.Cmd) {}
