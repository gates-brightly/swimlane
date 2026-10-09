package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
)

// cmdRunResults reads a `swim run --yaml` stream on stdin and prints
// "<lane> <PASS|FAIL|SKIP>" for every lane in its summary; with --raw, the
// summary's own words (passed, failed, skipped, interrupted). With
// --after-stop it instead prints the lanes whose start event came after
// the stop_requested event (none should).
func cmdRunResults(args []string) error {
	raw := len(args) > 0 && args[0] == "--raw"
	afterStop := len(args) > 0 && args[0] == "--after-stop"
	stopped := false
	dec := yaml.NewDecoder(os.Stdin)
	var sum *launcher.Summary
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if doc["schema"] != launcher.RunSchema {
			return fmt.Errorf("document without schema %s: %v", launcher.RunSchema, doc)
		}
		switch doc["event"] {
		case "stop_requested", "force_quit":
			stopped = true
		case "start":
			if afterStop && stopped {
				fmt.Println(doc["lane"])
			}
		}
		if doc["event"] == "summary" {
			data, _ := yaml.Marshal(doc)
			sum = &launcher.Summary{}
			if err := yaml.Unmarshal(data, sum); err != nil {
				return err
			}
		}
	}
	if sum == nil {
		return errors.New("no summary event in the stream")
	}
	if afterStop {
		return nil
	}
	words := map[string]string{"passed": "PASS", "failed": "FAIL", "skipped": "SKIP", "interrupted": "FAIL"}
	for _, l := range sum.Lanes {
		if raw {
			fmt.Println(l.Lane, l.Result)
			continue
		}
		fmt.Println(l.Lane, words[l.Result])
	}
	return nil
}

// cmdStepOutput reads `swim log N --yaml` on stdin and prints the output of
// the steps whose label contains LABEL.
func cmdStepOutput(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: e2etool step-output LABEL < round.yml")
	}
	var d logparse.Doc
	if err := yaml.NewDecoder(os.Stdin).Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, r := range d.Results {
		if r.Step && strings.Contains(r.Label, args[0]) {
			for _, line := range r.Output {
				fmt.Println(line)
			}
		}
	}
	return nil
}
