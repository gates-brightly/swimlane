// Package backup is the built-in backup module: a restore point taken
// right before a change. It starts an RDS snapshot or a DynamoDB backup
// named after the lane and job, waits until it's usable, and records its
// id as an output (backup.<kind>.<name>, or --key) for later lanes. Under
// mod_env it also gives the round BACKUP_ID, BACKUP_KIND and BACKUP_SOURCE,
// for an on_failure that restores from it. It changes nothing it backs up,
// but it does create something (and costs storage), so a read-only check
// (swim plan --check) skips it.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod backup rds INSTANCE | rds-cluster CLUSTER | dynamodb TABLE [--for 60m] [--every 15s] [--key K]"

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	keyClean = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
)

// kind is one kind of backup: how to start one and how to see its state.
type kind struct {
	start func(c *gomod.Ctx, source, name string) (id string, err error)
	state func(c *gomod.Ctx, id string) (status string, err error)
	ready string // the status that means usable
}

var kinds = map[string]kind{
	"rds": {
		start: func(c *gomod.Ctx, source, name string) (string, error) {
			_, err := c.Capture("aws", "rds", "create-db-snapshot", "--db-instance-identifier", source,
				"--db-snapshot-identifier", name, "--tags", tags(c)[0], tags(c)[1], "--output", "json")
			return name, err
		},
		state: func(c *gomod.Ctx, id string) (string, error) {
			var r struct{ DBSnapshots []struct{ Status string } }
			if err := capture(c, &r, "aws", "rds", "describe-db-snapshots", "--db-snapshot-identifier", id, "--output", "json"); err != nil {
				return "", err
			}
			if len(r.DBSnapshots) == 0 {
				return "", errors.New("aws rds describe-db-snapshots: no snapshot " + id)
			}
			return r.DBSnapshots[0].Status, nil
		},
		ready: "available",
	},
	"rds-cluster": {
		start: func(c *gomod.Ctx, source, name string) (string, error) {
			_, err := c.Capture("aws", "rds", "create-db-cluster-snapshot", "--db-cluster-identifier", source,
				"--db-cluster-snapshot-identifier", name, "--tags", tags(c)[0], tags(c)[1], "--output", "json")
			return name, err
		},
		state: func(c *gomod.Ctx, id string) (string, error) {
			var r struct{ DBClusterSnapshots []struct{ Status string } }
			if err := capture(c, &r, "aws", "rds", "describe-db-cluster-snapshots", "--db-cluster-snapshot-identifier", id, "--output", "json"); err != nil {
				return "", err
			}
			if len(r.DBClusterSnapshots) == 0 {
				return "", errors.New("aws rds describe-db-cluster-snapshots: no snapshot " + id)
			}
			return r.DBClusterSnapshots[0].Status, nil
		},
		ready: "available",
	},
	"dynamodb": {
		start: func(c *gomod.Ctx, source, name string) (string, error) {
			var r struct{ BackupDetails struct{ BackupArn string } }
			if err := capture(c, &r, "aws", "dynamodb", "create-backup", "--table-name", source, "--backup-name", name, "--output", "json"); err != nil {
				return "", err
			}
			if r.BackupDetails.BackupArn == "" {
				return "", errors.New("aws dynamodb create-backup returned no backup ARN")
			}
			return r.BackupDetails.BackupArn, nil
		},
		state: func(c *gomod.Ctx, id string) (string, error) {
			var r struct {
				BackupDescription struct{ BackupDetails struct{ BackupStatus string } }
			}
			if err := capture(c, &r, "aws", "dynamodb", "describe-backup", "--backup-arn", id, "--output", "json"); err != nil {
				return "", err
			}
			return strings.ToLower(r.BackupDescription.BackupDetails.BackupStatus), nil
		},
		ready: "available",
	},
}

// Run is mod backup rds|rds-cluster|dynamodb SOURCE ...
func Run(c *gomod.Ctx) error {
	a, err := gomod.ParseArgs(c.Args, []string{"for", "every", "key"}, nil)
	if err != nil {
		return err
	}
	if len(a.Pos) != 2 || len(a.Rest) > 0 {
		return gomod.Usage{Msg: usage}
	}
	k, ok := kinds[a.Pos[0]]
	source := a.Pos[1]
	if !ok {
		return gomod.Usage{Msg: usage}
	}
	if !nameRE.MatchString(source) {
		return gomod.Usage{Msg: fmt.Sprintf("backup: %q isn't a resource name", source)}
	}
	budget, err := duration(a, "for", 60*time.Minute)
	if err != nil {
		return err
	}
	every, err := duration(a, "every", 15*time.Second)
	if err != nil {
		return err
	}
	key := a.Flag("key", "backup."+a.Pos[0]+"."+keyClean.ReplaceAllString(source, "_"))
	if c.Getenv("SWIM_CHECK_ONLY") == "1" {
		fmt.Fprintf(c.Stdout, "backup: not taken: swim plan --check changes nothing (it would back up %s %s)\n", a.Pos[0], source)
		return nil
	}
	name := backupName(c, time.Now())
	fmt.Fprintf(c.Stdout, "backup: %s %s as %s\n", a.Pos[0], source, name)
	id, err := k.start(c, source, name)
	if err != nil {
		return err
	}
	start := time.Now()
	last := ""
	for {
		st, err := k.state(c, id)
		if err != nil {
			return err
		}
		if st != last {
			fmt.Fprintf(c.Stdout, "backup: %s (%s)\n", st, time.Since(start).Round(time.Second))
			last = st
		}
		if st == k.ready {
			break
		}
		if st == "failed" || st == "deleted" || st == "deleting" || st == "incompatible-restore" {
			return fmt.Errorf("backup %s is %s", id, st)
		}
		if time.Since(start)+every > budget {
			return fmt.Errorf("backup %s isn't %s after %s (still %s; it may finish later)", id, k.ready, budget, st)
		}
		time.Sleep(every)
	}
	fmt.Fprintf(c.Stdout, "backup: %s is ready: %s\n", a.Pos[0], id)
	if c.Output != nil {
		if err := c.Output(key, id); err != nil {
			return fmt.Errorf("record output %s: %w", key, err)
		}
		fmt.Fprintf(c.Stdout, "backup: recorded as output %s (later lanes: $(input %s %s))\n", key, c.Getenv("SWIM_LANE"), key)
	}
	if c.Out != nil {
		for _, kv := range [][2]string{{"BACKUP_ID", id}, {"BACKUP_KIND", a.Pos[0]}, {"BACKUP_SOURCE", source}} {
			if err := c.Set(kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// backupName names a backup after the lane, its job and the time:
// swim-3-3f2a9c1e-20261016-142233. It starts with a letter and has no
// double hyphen, as RDS requires.
func backupName(c *gomod.Ctx, now time.Time) string {
	parts := []string{"swim"}
	if n := c.Getenv("SWIM_LANE"); n != "" {
		parts = append(parts, n)
	}
	job := strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(c.Getenv("SWIM_JOB"), ""))
	if job != "" {
		parts = append(parts, job[:min(8, len(job))])
	}
	parts = append(parts, now.UTC().Format("20060102-150405"))
	name := strings.Join(parts, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return strings.Trim(name, "-.")
}

// tags are an RDS snapshot's tags: the job and lane that took it.
func tags(c *gomod.Ctx) [2]string {
	job := c.Getenv("SWIM_JOB")
	if job == "" {
		job = "none"
	}
	lane := c.Getenv("SWIM_LANE")
	if lane == "" {
		lane = "none"
	}
	return [2]string{"Key=swim-job,Value=" + job, "Key=swim-lane,Value=" + lane}
}

func capture(c *gomod.Ctx, v any, name string, args ...string) error {
	out, err := c.Capture(name, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("%s %s %s: %w", name, args[0], args[1], err)
	}
	return nil
}

func duration(a gomod.Args, name string, def time.Duration) (time.Duration, error) {
	v := a.Flag(name, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, gomod.Usage{Msg: fmt.Sprintf("--%s %q isn't a duration like 30s or 60m", name, v)}
	}
	return d, nil
}
