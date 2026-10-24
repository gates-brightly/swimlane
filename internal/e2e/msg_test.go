package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A round posts status messages from its script and from inside a step:
// the latest shows in status while it runs, the log gets them throttled
// (in-step ones in the step's block), and the round's end clears them.
func TestLaneMessages(t *testing.T) {
	r := newRepo(t, "")
	r.env = append(r.env, "DEPLOY_TOKEN=supersecret-token-value")
	r.script(1, "messages", `msg --progress 1/3 "starting"
msg --progress 1/3 "starting"
msg --progress 2/3 "throttled away"
run "inside" bash -c 'swim msg "from inside $DEPLOY_TOKEN"; printf "%s" "$(printf "\033[31mred\033[0m line")" >/dev/null; swim msg --progress 9/9 "$(printf "\033[31mcolored\033[0m\nmulti")"; touch "$SWIM_ROOT/posted"; while [ ! -e "$SWIM_ROOT/release" ]; do sleep 0.05; done'
msg ""
msg "after clear"`)
	l := r.launch(t, nil, "--yaml")
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(r.root, "posted")); return err == nil })
	time.Sleep(100 * time.Millisecond)
	if st := r.status().Get(1); st.Message != "colored multi" || st.Progress != "9/9" || st.MessageAt == "" {
		t.Errorf("status while running: %q %q %q", st.Message, st.Progress, st.MessageAt)
	}
	contains(t, "swim status", r.mustSwim("status"), "msg: colored multi (9/9)")
	time.Sleep(500 * time.Millisecond) // the live view polls every 200ms
	os.WriteFile(filepath.Join(r.root, "release"), nil, 0o644)
	l.wait(t, 20*time.Second)

	log := r.log(1)
	contains(t, "log", log, "  MSG  starting (1/3, ", "-- MSG  colored multi (9/9, ", "  MSG  after clear (")
	if strings.Count(log, "MSG  starting") != 1 || strings.Contains(log, "throttled away") {
		t.Errorf("throttling (msg_log_interval 10s):\n%s", log)
	}
	if strings.Contains(log, "supersecret-token-value") || strings.Contains(log, "\x1b") {
		t.Errorf("message not cleaned or masked:\n%s", log)
	}
	if st := r.status().Get(1); st.Message != "" || st.Progress != "" {
		t.Errorf("message outlived its round: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "msg", "lane1")); err == nil {
		t.Error(".swim/msg/lane1 still there")
	}
	contains(t, "yaml events", l.out.String(), "event: msg", "message: colored multi", "progress: 9/9")
	contains(t, "log --yaml", r.mustSwim("log", "1", "--yaml"), "messages:", "- colored multi (9/9, ", "result: MSG")

	// Never fails a round: outside a lane it warns and exits 0.
	out, code := r.swim("msg", "hello")
	if code != 0 || !strings.Contains(out, "warning: not inside a lane's round") {
		t.Errorf("outside a lane: exit %d\n%s", code, out)
	}
}

// msg_log_interval 0 logs every change; a lane in another language posts
// with swim msg.
func TestLaneMessagesEveryChange(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "msg_log_interval", "0")
	r.write("lane.1.sh", "#!/usr/bin/env bash\n# Round: wrapped\nfor i in 1 2 3; do swim msg --progress $i/3 \"step $i\"; done\nswim run \"ok\" -- true\n", 0o755)
	r.mustSwim("run", "1", "--plain")
	log := r.log(1)
	for _, want := range []string{"MSG  step 1 (1/3", "MSG  step 2 (2/3", "MSG  step 3 (3/3"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q:\n%s", want, log)
		}
	}
}
