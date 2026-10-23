package config

import (
	"reflect"
	"testing"
)

func TestHubAllow(t *testing.T) {
	none := &Config{}
	narrow := &Config{Headless: &Headless{Allow: []string{HubLog, HubInterrupt, HubQueue}}}
	for _, c := range []struct {
		cfg    *Config
		access string
		want   []string
	}{
		{none, AccessRead, []string{HubLog}},
		{none, AccessReadWrite, HubCommands},
		{none, "", HeadlessDefaultAllow}, // from before access levels
		{narrow, AccessRead, []string{HubLog}},
		{narrow, AccessReadWrite, []string{HubInterrupt, HubLog, HubQueue}},
		{narrow, "", []string{HubLog, HubInterrupt, HubQueue}},
	} {
		if got := c.cfg.HubAllow(c.access); !reflect.DeepEqual(got, c.want) {
			t.Errorf("HubAllow(%q) with %+v = %v, want %v", c.access, c.cfg.Headless, got, c.want)
		}
	}
	if none.HubAllows(AccessRead, HubAnswer) || !none.HubAllows(AccessReadWrite, HubRequeue) {
		t.Error("HubAllows")
	}
	if !ValidAccess("read") || !ValidAccess("readwrite") || ValidAccess("write") || ValidAccess("") {
		t.Error("ValidAccess")
	}
}
