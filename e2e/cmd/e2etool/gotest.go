package main

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"sort"
	"strings"
)

// cmdGotestReport reads `go test -json` (stdin), prints the output as it
// comes, then ends with every failed test and its output, so the last
// lines of a red CI step name the test (T33). --junit FILE also writes a
// JUnit report with one case per Go test. Exits 1 when anything failed.
//
//	go test -json ./... | e2etool gotest-report [--junit FILE]
func cmdGotestReport(args []string) error {
	junit := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--junit" && i+1 < len(args) {
			junit = args[i+1]
			i++
		}
	}
	type event struct {
		Action, Package, Test, Output string
		Elapsed                       float64
	}
	type result struct {
		pkg, test, action string
		elapsed           float64
		output            []string
	}
	results := map[string]*result{}
	var order []string
	get := func(pkg, test string) *result {
		k := pkg + "\x00" + test
		if results[k] == nil {
			results[k] = &result{pkg: pkg, test: test}
			order = append(order, k)
		}
		return results[k]
	}
	failed := false
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var e event
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &e) != nil {
			fmt.Println(string(line)) // not test2json (a build error): as is
			continue
		}
		switch e.Action {
		case "output", "build-output":
			fmt.Print(e.Output)
			r := get(e.Package, e.Test)
			r.output = append(r.output, strings.TrimRight(e.Output, "\n"))
		case "pass", "fail", "skip":
			r := get(e.Package, e.Test)
			r.action, r.elapsed = e.Action, e.Elapsed
			if e.Action == "fail" {
				failed = true
			}
		case "build-fail":
			failed = true
		}
	}
	var fails []*result
	for _, k := range order {
		if r := results[k]; r.action == "fail" && r.test != "" {
			fails = append(fails, r)
		}
	}
	if len(fails) > 0 {
		fmt.Printf("\n=== %d failed tests\n", len(fails))
		for _, r := range fails {
			fmt.Printf("\n--- FAIL: %s (%s, %.1fs)\n", r.test, r.pkg, r.elapsed)
			out := r.output
			if len(out) > 80 {
				fmt.Printf("    ... %d lines before\n", len(out)-80)
				out = out[len(out)-80:]
			}
			for _, l := range out {
				fmt.Println("    " + l)
			}
		}
		var names []string
		for _, r := range fails {
			names = append(names, r.test)
		}
		fmt.Printf("\nFAILED: %s\n", strings.Join(names, " "))
	}
	if junit != "" {
		if err := writeJUnit(junit, order, func(k string) (pkg, test, action string, elapsed float64, out []string) {
			r := results[k]
			return r.pkg, r.test, r.action, r.elapsed, r.output
		}); err != nil {
			return err
		}
	}
	if failed {
		return exitCode(1)
	}
	return nil
}

func writeJUnit(path string, order []string, get func(string) (string, string, string, float64, []string)) error {
	type failure struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	}
	type tc struct {
		Name    string    `xml:"name,attr"`
		Class   string    `xml:"classname,attr"`
		Time    string    `xml:"time,attr"`
		Failure *failure  `xml:"failure,omitempty"`
		Skipped *struct{} `xml:"skipped,omitempty"`
	}
	type suite struct {
		XMLName  xml.Name `xml:"testsuite"`
		Name     string   `xml:"name,attr"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
		Cases    []tc     `xml:"testcase"`
	}
	suites := map[string]*suite{}
	for _, k := range order {
		pkg, test, action, elapsed, out := get(k)
		if test == "" || action == "" {
			continue
		}
		s := suites[pkg]
		if s == nil {
			s = &suite{Name: pkg}
			suites[pkg] = s
		}
		c := tc{Name: test, Class: pkg, Time: fmt.Sprintf("%.3f", elapsed)}
		switch action {
		case "fail":
			c.Failure = &failure{Message: "failed", Text: strings.Join(out, "\n")}
			s.Failures++
		case "skip":
			c.Skipped = &struct{}{}
		}
		s.Cases = append(s.Cases, c)
		s.Tests++
	}
	var names []string
	for n := range suites {
		names = append(names, n)
	}
	sort.Strings(names)
	doc := struct {
		XMLName xml.Name `xml:"testsuites"`
		Suites  []*suite
	}{}
	for _, n := range names {
		doc.Suites = append(doc.Suites, suites[n])
	}
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), data...), 0o644)
}
