//go:build !unix

package kiro

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func killGroup(*exec.Cmd) {}
