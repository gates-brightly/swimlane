package backup

import (
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

// fakeAWS answers create with nothing and describe with "creating" until
// it has been asked twice, then with $FINAL (default available).
const fakeAWS = `echo "$*" >>"$FAKE_DIR/calls"
n=$(cat "$FAKE_DIR/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n >"$FAKE_DIR/n"
st=creating; [ "$n" -ge 3 ] && st=${FINAL:-available}
case "$1 $2" in
  "rds create-db-snapshot"|"rds create-db-cluster-snapshot") echo '{}' ;;
  "rds describe-db-snapshots") echo "{\"DBSnapshots\": [{\"Status\": \"$st\"}]}" ;;
  "rds describe-db-cluster-snapshots") echo "{\"DBClusterSnapshots\": [{\"Status\": \"$st\"}]}" ;;
  "dynamodb create-backup") echo '{"BackupDetails": {"BackupArn": "arn:aws:dynamodb:us-east-1:1:table/orders/backup/0167"}}' ;;
  "dynamodb describe-backup") echo "{\"BackupDescription\": {\"BackupDetails\": {\"BackupStatus\": \"$(echo $st | tr a-z A-Z)\"}}}" ;;
  *) echo "unexpected $*" >&2; exit 1 ;;
esac`

func newFake(t *testing.T, env map[string]string, withOut bool, args ...string) *gomodtest.Fake {
	base := map[string]string{"SWIM_LANE": "3", "SWIM_JOB": "3f2a9c1e-7b4d-4e2a"}
	for k, v := range env {
		base[k] = v
	}
	f := gomodtest.NewFake(t.TempDir(), base, withOut, args...)
	f.Tool("aws", fakeAWS)
	return f
}

func TestBackupRDS(t *testing.T) {
	f := newFake(t, nil, true, "rds", "orders-prod", "--every", "10ms")
	if err := Run(&f.Ctx); err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
	out := f.StdoutB.String()
	for _, want := range []string{"backup: rds orders-prod as swim-3-3f2a9c1e-", "backup: creating", "backup: available", "recorded as output backup.rds.orders-prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	id := f.Outputs["backup.rds.orders-prod"]
	if !strings.HasPrefix(id, "swim-3-3f2a9c1e-") {
		t.Errorf("outputs: %v", f.Outputs)
	}
	if got := f.OutB.String(); got != "BACKUP_ID="+id+"\nBACKUP_KIND=rds\nBACKUP_SOURCE=orders-prod\n" {
		t.Errorf("env: %q", got)
	}
}

func TestBackupDynamoAndCluster(t *testing.T) {
	f := newFake(t, nil, false, "dynamodb", "orders", "--every", "10ms", "--key", "pre_change")
	if err := Run(&f.Ctx); err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
	if f.Outputs["pre_change"] != "arn:aws:dynamodb:us-east-1:1:table/orders/backup/0167" {
		t.Errorf("outputs: %v", f.Outputs)
	}
	f = newFake(t, nil, false, "rds-cluster", "orders-aurora", "--every", "10ms")
	if err := Run(&f.Ctx); err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
}

func TestBackupFailsAndChecks(t *testing.T) {
	f := newFake(t, map[string]string{"FINAL": "failed"}, false, "rds", "orders-prod", "--every", "10ms")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "is failed") {
		t.Errorf("failed: %v", err)
	}
	f = newFake(t, map[string]string{"FINAL": "creating"}, false, "rds", "orders-prod", "--every", "20ms", "--for", "60ms")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "isn't available after") {
		t.Errorf("budget: %v", err)
	}
	f = newFake(t, map[string]string{"SWIM_CHECK_ONLY": "1"}, false, "rds", "orders-prod")
	if err := Run(&f.Ctx); err != nil || !strings.Contains(f.StdoutB.String(), "not taken: swim plan --check") || len(f.Outputs) != 0 {
		t.Errorf("check mode: %v %s", err, f.StdoutB.String())
	}
	f = newFake(t, nil, false, "s3", "bucket")
	if err := Run(&f.Ctx); err == nil {
		t.Error("an unknown kind passed")
	}
}

func TestBackupName(t *testing.T) {
	f := gomodtest.NewFake(t.TempDir(), map[string]string{"SWIM_LANE": "12", "SWIM_JOB": "AB_cd-12--34"}, false)
	if got := backupName(&f.Ctx, time.Date(2026, 10, 16, 14, 22, 33, 0, time.UTC)); got != "swim-12-abcd1234-20261016-142233" {
		t.Errorf("name: %s", got)
	}
}
