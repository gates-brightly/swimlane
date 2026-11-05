package cli

import (
	"os"
	"testing"
)

func TestConfigShown(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct{ key, v, want string }{
		{"term_grace", "10s", "10s"},
		{"queue_lease", "2m0s", "2m"},
		{"claim_lease", "2h0m0s", "2h"},
		{"claim_lease", "1h30m0s", "1h30m"},
		{"msg_log_interval", "0s", "0s"},
		{"outputs_keep", "336h0m0s", "14d"},
		{"outputs_keep", "36h0m0s", "36h"},
		{"store_dir", home + "/.local/share/swim/store", "~/.local/share/swim/store"},
		{"store_dir", "off", "off"},
		{"signing_key", "", "—"},
	} {
		if got := configShown(c.key, c.v); got != c.want {
			t.Errorf("configShown(%s, %s) = %q, want %q", c.key, c.v, got, c.want)
		}
	}
}
