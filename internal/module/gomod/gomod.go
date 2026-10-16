// Package gomod is what a Go module built into swim gets when it runs: its
// arguments, its streams, the lane's environment, and where to write the
// variables it gives the lane (mod_env).
package gomod

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
	// Output records a small value among the round's outputs (swim output
	// KEY VALUE), for later lanes ($(input N KEY)).
	Output func(key, value string) error
}

// Set writes one variable for the lane (mod_env).
func (c *Ctx) Set(key, value string) error {
	if c.Out == nil {
		return fmt.Errorf("this module gives the lane variables: call it with mod_env, not mod")
	}
	_, err := fmt.Fprintf(c.Out, "%s=%s\n", key, value)
	return err
}

// Capture runs a tool with the lane's environment and returns its
// standard output; an error says what failed, with the tool's stderr.
func (c *Ctx) Capture(name string, args ...string) ([]byte, error) {
	cmd := c.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		what := name
		for i := 0; i < len(args) && i < 2; i++ {
			what += " " + args[i]
		}
		return nil, fmt.Errorf("%s: %s", what, msg)
	}
	return out.Bytes(), nil
}

// Usage is an error that prints as the module's usage (exit 2).
type Usage struct{ Msg string }

func (u Usage) Error() string { return u.Msg }

// Args are a command line read by ParseArgs.
type Args struct {
	Pos   []string          // positional arguments, in order
	Flags map[string]string // --name value, --name=value; "true" for a switch
	Rest  []string          // what follows --
}

// Flag is a flag's value, or def.
func (a Args) Flag(name, def string) string {
	if v, ok := a.Flags[name]; ok {
		return v
	}
	return def
}

// ParseArgs reads args: --name value for the names in values, --name for
// the names in switches, positional arguments anywhere, and everything
// after -- as Rest. An unknown --flag is a usage error.
func ParseArgs(args []string, values, switches []string) (Args, error) {
	a := Args{Flags: map[string]string{}}
	known := func(list []string, n string) bool {
		for _, x := range list {
			if x == n {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			a.Rest = args[i+1:]
			return a, nil
		case strings.HasPrefix(arg, "--"):
			name, val, has := strings.Cut(arg[2:], "=")
			switch {
			case known(switches, name) && !has:
				a.Flags[name] = "true"
			case known(values, name) && has:
				a.Flags[name] = val
			case known(values, name):
				if i+1 >= len(args) {
					return a, Usage{Msg: "--" + name + " needs a value"}
				}
				a.Flags[name] = args[i+1]
				i++
			default:
				return a, Usage{Msg: "unknown option " + arg}
			}
		default:
			a.Pos = append(a.Pos, arg)
		}
	}
	return a, nil
}
