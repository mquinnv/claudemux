package main

import (
	"errors"
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

func TestResolveWebListenPassesPlainAddressThrough(t *testing.T) {
	called := false
	stub := func() (string, error) { called = true; return "100.64.0.15", nil }
	addr, err := resolveWebListen(webListen{Host: "127.0.0.1", Port: "7474"}, stub)
	if err != nil || addr != "127.0.0.1:7474" {
		t.Fatalf("got %q, %v; want 127.0.0.1:7474", addr, err)
	}
	if called {
		t.Error("tailscale resolver called for a plain address")
	}
	addr, err = resolveWebListen(webListen{Host: "", Port: "7474"}, stub)
	if err != nil || addr != ":7474" {
		t.Fatalf("got %q, %v; want :7474", addr, err)
	}
}

func TestResolveWebListenTailscale(t *testing.T) {
	ok := func() (string, error) { return "100.64.0.15", nil }
	addr, err := resolveWebListen(webListen{Host: "tailscale", Port: "7474", Tailscale: true}, ok)
	if err != nil || addr != "100.64.0.15:7474" {
		t.Fatalf("got %q, %v; want 100.64.0.15:7474", addr, err)
	}
	bad := func() (string, error) { return "", errors.New("exec: tailscale: not found") }
	_, err = resolveWebListen(webListen{Host: "tailscale", Port: "7474", Tailscale: true}, bad)
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("err = %v, want one naming tailscale", err)
	}
}

func TestParseTailscaleStatus(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    webAllow
		errPart string
	}{
		{name: "ordinary suffix", out: `{"MagicDNSSuffix":"nodes.headscale.mage.net"}`, want: webAllow{Suffix: "nodes.headscale.mage.net"}},
		{name: "trailing dot and mixed case", out: `{"MagicDNSSuffix":"Nodes.Headscale.Mage.Net."}`, want: webAllow{Suffix: "nodes.headscale.mage.net"}},
		{name: "magicdns off", out: `{"MagicDNSSuffix":""}`, want: webAllow{}},
		{name: "field absent", out: `{"Self":{}}`, want: webAllow{}},
		{name: "not json", out: "tailscale is stopped", errPart: "tailscale status"},
		{
			name: "Self.DNSName present with trailing dot",
			out:  `{"MagicDNSSuffix":"nodes.headscale.mage.net","Self":{"DNSName":"michaels-claudes.nodes.headscale.mage.net."}}`,
			want: webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"},
		},
		{
			name: "missing Self entirely, suffix-only JSON still parses",
			out:  `{"MagicDNSSuffix":"nodes.headscale.mage.net"}`,
			want: webAllow{Suffix: "nodes.headscale.mage.net"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTailscaleStatus(c.out)
			if c.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("err = %v, want one mentioning %q", err, c.errPart)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("parseTailscaleStatus(%q) = %+v, %v; want %+v", c.out, got, err, c.want)
			}
		})
	}
}

func TestParseTailscaleIP(t *testing.T) {
	cases := []struct {
		out     string
		want    string
		errPart string
	}{
		{out: "100.64.0.15\n", want: "100.64.0.15"},
		{out: "\n  100.64.0.15  \nfd7a::1\n", want: "100.64.0.15"},
		{out: "", errPart: "printed nothing"},
		{out: "\n\n", errPart: "printed nothing"},
		{out: "Tailscale is stopped.\n", errPart: "not an address"},
	}
	for _, c := range cases {
		got, err := parseTailscaleIP(c.out)
		if c.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), c.errPart) {
				t.Errorf("parseTailscaleIP(%q) err = %v, want %q", c.out, err, c.errPart)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseTailscaleIP(%q) = %q, %v; want %q", c.out, got, err, c.want)
		}
	}
}
