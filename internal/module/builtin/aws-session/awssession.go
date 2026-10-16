// Package awssession is the built-in aws-session module: AWS credentials
// from the operator's own profile or SSO, optionally assuming a role, given
// to the lane's later steps (mod_env). It asks the AWS CLI, so it works
// with whatever the profile is (keys, SSO, a credential process).
package awssession

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

type creds struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

var sessionRE = regexp.MustCompile(`[^\w+=,.@-]`)

// Run is mod_env aws-session --profile P [--role ARN] [--duration D]
// [--region R] [--session-name N].
func Run(c *gomod.Ctx) error {
	fs := flag.NewFlagSet("aws-session", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "")
	role := fs.String("role", "", "")
	duration := fs.Duration("duration", time.Hour, "")
	region := fs.String("region", "", "")
	session := fs.String("session-name", "", "")
	usage := gomod.Usage{Msg: "usage: mod_env aws-session --profile P [--role ARN] [--duration 1h] [--region R] [--session-name N]"}
	if err := fs.Parse(c.Args); err != nil || fs.NArg() > 0 || *profile == "" {
		return usage
	}
	if *duration < 15*time.Minute || *duration > 12*time.Hour {
		return gomod.Usage{Msg: "aws-session: --duration is between 15m and 12h"}
	}
	if c.Out == nil {
		return errors.New("aws-session gives the lane credentials: call it with mod_env, not mod")
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return errors.New("aws-session needs the AWS CLI (aws) on PATH")
	}
	var cr creds
	if *role == "" {
		fmt.Fprintf(c.Stdout, "aws-session: profile %s\n", *profile)
		out, err := aws(c, "configure", "export-credentials", "--profile", *profile, "--format", "process")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(out, &cr); err != nil {
			return fmt.Errorf("aws configure export-credentials: %w", err)
		}
	} else {
		name := *session
		if name == "" {
			name = "swim-" + c.Getenv("SWIM_JOB")
			if name == "swim-" {
				name = "swim"
			}
		}
		name = sessionRE.ReplaceAllString(name, "-")
		if len(name) > 64 {
			name = name[:64]
		}
		fmt.Fprintf(c.Stdout, "aws-session: profile %s, assuming %s as %s for %s\n", *profile, *role, name, *duration)
		args := []string{"sts", "assume-role", "--profile", *profile, "--role-arn", *role, "--role-session-name", name,
			"--duration-seconds", strconv.Itoa(int(duration.Seconds())), "--output", "json"}
		if *region != "" {
			args = append(args, "--region", *region)
		}
		out, err := aws(c, args...)
		if err != nil {
			return err
		}
		var resp struct{ Credentials creds }
		if err := json.Unmarshal(out, &resp); err != nil {
			return fmt.Errorf("aws sts assume-role: %w", err)
		}
		cr = resp.Credentials
	}
	if cr.AccessKeyID == "" || cr.SecretAccessKey == "" {
		return errors.New("aws-session: the AWS CLI returned no credentials")
	}
	vars := [][2]string{{"AWS_ACCESS_KEY_ID", cr.AccessKeyID}, {"AWS_SECRET_ACCESS_KEY", cr.SecretAccessKey}}
	if cr.SessionToken != "" {
		vars = append(vars, [2]string{"AWS_SESSION_TOKEN", cr.SessionToken})
	}
	if cr.Expiration != "" {
		vars = append(vars, [2]string{"AWS_CREDENTIAL_EXPIRATION", cr.Expiration})
	}
	if *region != "" {
		vars = append(vars, [2]string{"AWS_REGION", *region}, [2]string{"AWS_DEFAULT_REGION", *region})
	}
	for _, v := range vars {
		if err := c.Set(v[0], v[1]); err != nil {
			return err
		}
	}
	if cr.Expiration != "" {
		fmt.Fprintf(c.Stdout, "aws-session: credentials until %s\n", cr.Expiration)
	}
	return nil
}

// aws runs the AWS CLI and returns its output; its errors go to the step.
func aws(c *gomod.Ctx, args ...string) ([]byte, error) {
	cmd := c.Command("aws", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("aws %s: %s", strings.Join(args[:2], " "), msg)
	}
	return out.Bytes(), nil
}
