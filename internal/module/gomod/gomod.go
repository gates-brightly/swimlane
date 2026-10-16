// Package gomod is what a Go module built into swim gets when it runs: its
// arguments, its streams, the lane's environment, and where to write the
// variables it gives the lane (mod_env).
package gomod

import (
	"fmt"
	"io"
	"os/exec"
)

// Ctx is one call of a built-in Go module.
type Ctx struct {
	Args           []string
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Out takes KEY=VALUE lines for the lane's environment (mod_env); nil
	// under mod, which takes none.
	Out io.Writer
	// Command makes a command with the lane's environment.
	Command func(name string, args ...string) *exec.Cmd
}

// Set writes one variable for the lane (mod_env).
func (c *Ctx) Set(key, value string) error {
	if c.Out == nil {
		return fmt.Errorf("this module gives the lane variables: call it with mod_env, not mod")
	}
	_, err := fmt.Fprintf(c.Out, "%s=%s\n", key, value)
	return err
}

// Usage is an error that prints as the module's usage (exit 2).
type Usage struct{ Msg string }

func (u Usage) Error() string { return u.Msg }
