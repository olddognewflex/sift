//go:build !unix

package proc

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func killGroup(*exec.Cmd) {}
