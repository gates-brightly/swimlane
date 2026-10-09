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
// "<lane> <PASS|FAIL|SKIP>" for every lane in its summary.
func cmdRunResults(args []string) error {
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
	words := map[string]string{"passed": "PASS", "failed": "FAIL", "skipped": "SKIP", "interrupted": "FAIL"}
	for _, l := range sum.Lanes {
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
