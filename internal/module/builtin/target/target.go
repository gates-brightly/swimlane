// Package target is the built-in target module: it fails unless the
// account, cluster or project the lane's credentials point at is the one
// the script names, so a round can't run against the wrong environment.
// It asks the tools themselves (aws, kubectl, gcloud), with the lane's
// environment, which is what the steps after it will use. Use it as a gate:
// mod --gate target aws --account 123456789012.
package target

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod --gate target aws --account ID [--region R] [--profile P] | kube --context C [--namespace N] [--server URL] | gcp --project P [--account A]"

// Run is mod target aws|kube|gcp ...
func Run(c *gomod.Ctx) error {
	a, err := gomod.ParseArgs(c.Args, []string{"account", "region", "profile", "context", "namespace", "server", "project"}, nil)
	if err != nil {
		return err
	}
	if len(a.Pos) != 1 || len(a.Rest) > 0 {
		return gomod.Usage{Msg: usage}
	}
	var checks []check
	switch a.Pos[0] {
	case "aws":
		checks, err = awsChecks(c, a)
	case "kube":
		checks, err = kubeChecks(c, a)
	case "gcp":
		checks, err = gcpChecks(c, a)
	default:
		return gomod.Usage{Msg: usage}
	}
	if err != nil {
		return err
	}
	var wrong []string
	for _, k := range checks {
		if k.got == k.want {
			fmt.Fprintf(c.Stdout, "target: %s %s, as the lane says\n", k.what, k.got)
			continue
		}
		got := k.got
		if got == "" {
			got = "(none)"
		}
		fmt.Fprintf(c.Stdout, "target: %s is %s, not %s\n", k.what, got, k.want)
		wrong = append(wrong, fmt.Sprintf("%s is %s, not %s", k.what, got, k.want))
	}
	if len(wrong) > 0 {
		return errors.New("wrong target: " + strings.Join(wrong, "; "))
	}
	return nil
}

type check struct{ what, want, got string }

func awsChecks(c *gomod.Ctx, a gomod.Args) ([]check, error) {
	want := a.Flag("account", "")
	if len(want) != 12 || !chars.Digits(want) {
		return nil, gomod.Usage{Msg: "target aws needs --account, a 12-digit account id"}
	}
	var prof []string
	if p := a.Flag("profile", ""); p != "" {
		prof = []string{"--profile", p}
	}
	out, err := c.Capture("aws", append([]string{"sts", "get-caller-identity", "--output", "json"}, prof...)...)
	if err != nil {
		return nil, err
	}
	var id struct{ Account, Arn string }
	if err := json.Unmarshal(out, &id); err != nil {
		return nil, fmt.Errorf("aws sts get-caller-identity: %w", err)
	}
	if id.Arn != "" {
		fmt.Fprintf(c.Stdout, "target: AWS identity %s\n", id.Arn)
	}
	checks := []check{{"AWS account", want, id.Account}}
	if r := a.Flag("region", ""); r != "" {
		got := c.Getenv("AWS_REGION")
		if got == "" {
			got = c.Getenv("AWS_DEFAULT_REGION")
		}
		if got == "" {
			o, _ := c.Capture("aws", append([]string{"configure", "get", "region"}, prof...)...)
			got = strings.TrimSpace(string(o))
		}
		checks = append(checks, check{"AWS region", r, got})
	}
	return checks, nil
}

func kubeChecks(c *gomod.Ctx, a gomod.Args) ([]check, error) {
	want := a.Flag("context", "")
	if want == "" {
		return nil, gomod.Usage{Msg: "target kube needs --context"}
	}
	out, err := c.Capture("kubectl", "config", "current-context")
	if err != nil {
		return nil, err
	}
	checks := []check{{"kube context", want, strings.TrimSpace(string(out))}}
	if ns := a.Flag("namespace", ""); ns != "" {
		o, err := c.Capture("kubectl", "config", "view", "--minify", "-o", "jsonpath={..namespace}")
		if err != nil {
			return nil, err
		}
		got := strings.TrimSpace(string(o))
		if got == "" {
			got = "default"
		}
		checks = append(checks, check{"kube namespace", ns, got})
	}
	if srv := a.Flag("server", ""); srv != "" {
		o, err := c.Capture("kubectl", "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
		if err != nil {
			return nil, err
		}
		checks = append(checks, check{"kube API server", srv, strings.TrimSpace(string(o))})
	}
	return checks, nil
}

func gcpChecks(c *gomod.Ctx, a gomod.Args) ([]check, error) {
	want := a.Flag("project", "")
	if want == "" {
		return nil, gomod.Usage{Msg: "target gcp needs --project"}
	}
	out, err := c.Capture("gcloud", "config", "get-value", "project")
	if err != nil {
		return nil, err
	}
	checks := []check{{"GCP project", want, strings.TrimSpace(string(out))}}
	if acct := a.Flag("account", ""); acct != "" {
		o, err := c.Capture("gcloud", "config", "get-value", "account")
		if err != nil {
			return nil, err
		}
		checks = append(checks, check{"GCP account", acct, strings.TrimSpace(string(o))})
	}
	return checks, nil
}
