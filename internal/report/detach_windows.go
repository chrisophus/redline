//go:build windows

package report

import "os/exec"

func detach(cmd *exec.Cmd) {
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
}
