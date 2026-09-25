package main

import (
	"strings"
	"testing"
)

func TestParseWebListen(t *testing.T) {
	cases := []struct {
		in        string
		on        bool
		host      string
		port      string
		tailscale bool
		errPart   string
	}{
		{in: "", on: false},
		{in: "tailscale:7474", on: true, host: "tailscale", port: "7474", tailscale: true},
		{in: "127.0.0.1:7474", on: true, host: "127.0.0.1", port: "7474"},
		{in: ":7474", on: true, host: "", port: "7474"},
		{in: "tailscale", errPart: "host:port"},
		{in: "tailscale:", errPart: "port"},
		{in: "tailscale:http", errPart: "port"},
		{in: "tailscale:70000", errPart: "port"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			l, on, err := parseWebListen(c.in)
			if c.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("parseWebListen(%q) err = %v, want one mentioning %q", c.in, err, c.errPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWebListen(%q) err = %v", c.in, err)
			}
			if on != c.on {
				t.Fatalf("on = %v, want %v", on, c.on)
			}
			if l.Host != c.host || l.Port != c.port || l.Tailscale != c.tailscale {
				t.Errorf("got %+v, want host=%q port=%q tailscale=%v", l, c.host, c.port, c.tailscale)
			}
		})
	}
}
