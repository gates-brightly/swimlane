package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The queue entry state machine (docs/state.md), as the rules every move
// of an entry's file is checked against. An entry's state is where its
// file is: NNNN-<id>.queue waiting, .running claimed (orphaned when its
// worker is gone), done/ consumed.
//
//	queued  -> running   a worker claims it
//	running -> done      the worker consumes it with its result
//	running -> queued    requeued, once orphaned (Requeue refuses a live claim)
//	done    -> queued    requeued, to run again
//	queued, running, done -> removed   swim queue rm (not a live claim)
//
// Anything else (consuming an entry no worker claimed, claiming one twice,
// moving a consumed entry but to requeue or remove it) is a bug: it panics
// in tests and wherever SWIM_CHECK_STATES=1 is set, and is refused
// elsewhere.

// Removed is an entry's state once swim queue rm deletes it.
const Removed = "removed"

// checkEnv is the switch status uses too.
const checkEnv = "SWIM_CHECK_STATES"

var legal = map[[2]string]bool{
	{Queued, Running}:  true,
	{Running, Done}:    true,
	{Running, Queued}:  true,
	{Done, Queued}:     true,
	{Queued, Removed}:  true,
	{Running, Removed}: true,
	{Done, Removed}:    true,
}

// Transition checks one entry's move from state from to state to.
func Transition(from, to string) error {
	if legal[[2]string{from, to}] {
		return nil
	}
	return fmt.Errorf("a queue entry can't go from %s to %s", or(from, "nowhere"), to)
}

// fileState is the state an entry's file says it is in.
func fileState(path string) string {
	switch {
	case filepath.Base(filepath.Dir(path)) == "done":
		return Done
	case strings.HasSuffix(path, ".running"):
		return Running
	case strings.HasSuffix(path, ".queue"):
		return Queued
	}
	return ""
}

// move checks an entry's move: an illegal one panics where transitions are
// checked (tests, SWIM_CHECK_STATES=1), and is an error elsewhere.
func move(id, path, to string) error {
	err := Transition(fileState(path), to)
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%s: %w (docs/state.md)", id, err)
	if testing.Testing() || os.Getenv(checkEnv) == "1" {
		panic("queue: illegal state transition: " + err.Error())
	}
	return err
}
