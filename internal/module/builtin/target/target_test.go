package target

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

func TestTarget(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
		want string // "" passes; else the error contains it
		out  string
	}{
		{"aws match", nil, []string{"aws", "--account", "123456789012"}, "", "target: AWS account 123456789012, as the lane says"},
		{"aws wrong account", nil, []string{"aws", "--account", "999999999999"}, "AWS account is 123456789012, not 999999999999", "arn:aws:iam::123456789012:user/ops"},
		{"aws region from env", map[string]string{"AWS_REGION": "eu-west-1"}, []string{"aws", "--account", "123456789012", "--region", "us-east-1"}, "AWS region is eu-west-1, not us-east-1", ""},
		{"aws region from config", nil, []string{"aws", "--account", "123456789012", "--region", "us-east-2"}, "", "AWS region us-east-2"},
		{"aws bad account", nil, []string{"aws", "--account", "12"}, "12-digit", ""},
		{"kube match", nil, []string{"kube", "--context", "prod-east", "--namespace", "orders", "--server", "https://k8s.prod:6443"}, "", "kube namespace orders"},
		{"kube wrong context", nil, []string{"kube", "--context", "staging"}, "kube context is prod-east, not staging", ""},
		{"gcp wrong project", nil, []string{"gcp", "--project", "orders-prod"}, "GCP project is orders-dev, not orders-prod", ""},
		{"usage", nil, []string{"azure"}, "usage:", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := c.env
			if env == nil {
				env = map[string]string{}
			}
			f := gomodtest.NewFake(t.TempDir(), env, false, c.args...)
			f.Tool("aws", `case "$1 $2" in
  "sts get-caller-identity") echo '{"Account": "123456789012", "Arn": "arn:aws:iam::123456789012:user/ops"}' ;;
  "configure get") echo us-east-2 ;;
  *) exit 1 ;;
esac`)
			f.Tool("kubectl", `case "$*" in
  "config current-context") echo prod-east ;;
  *namespace*) echo orders ;;
  *server*) echo https://k8s.prod:6443 ;;
esac`)
			f.Tool("gcloud", `echo orders-dev`)
			err := Run(&f.Ctx)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("%v\n%s", err, f.StdoutB.String())
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err %v, want %q\n%s", err, c.want, f.StdoutB.String())
			}
			if !strings.Contains(f.StdoutB.String(), c.out) {
				t.Errorf("output lacks %q:\n%s", c.out, f.StdoutB.String())
			}
		})
	}
}
