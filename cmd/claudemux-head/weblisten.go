package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// webTailscaleHost is the keyword host in web.listen that means "this node's
// Tailscale IPv4", resolved at lobby start (see resolveWebListen).
const webTailscaleHost = "tailscale"

// webListen is a parsed web.listen value.
type webListen struct {
	Host      string // as written; "" means every interface
	Port      string
	Tailscale bool // Host was the tailscale keyword
}

// parseWebListen validates web.listen. "" is the off switch (on=false, no
// error). Anything else must be host:port with a numeric port: a value tmux
// or net.Listen would reject is refused here, where the message can name the
// key, rather than at bind time in a running lobby.
func parseWebListen(s string) (webListen, bool, error) {
	if s == "" {
		return webListen{}, false, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return webListen{}, false, fmt.Errorf("web.listen is %q: must be host:port, like \"tailscale:7474\" or \"127.0.0.1:7474\"", s)
	}
	// 0 is legal: like net.Listen, it means "let the OS pick a free port",
	// which is how the tests (and TestStartSwitchboardWebBindsAndReportsResolveFailure
	// in particular) bind without racing for a fixed one.
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return webListen{}, false, fmt.Errorf("web.listen is %q: port must be a number from 0 to 65535", s)
	}
	return webListen{Host: host, Port: port, Tailscale: host == webTailscaleHost}, true, nil
}

// resolveWebListen turns a parsed web.listen into the address net.Listen
// binds. Only the tailscale keyword needs resolving; tailscaleIP is a
// parameter so tests never run the binary.
func resolveWebListen(l webListen, tailscaleIP func() (string, error)) (string, error) {
	if !l.Tailscale {
		return net.JoinHostPort(l.Host, l.Port), nil
	}
	ip, err := tailscaleIP()
	if err != nil {
		return "", fmt.Errorf("web.listen %s:%s: %w", webTailscaleHost, l.Port, err)
	}
	return net.JoinHostPort(ip, l.Port), nil
}

// tailscaleIPv4 asks the tailscale CLI for this node's IPv4. Bounded by a
// timeout because it runs at lobby start, before the TUI is up: a hung CLI
// must not hang the launch.
func tailscaleIPv4() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale ip -4: %w", err)
	}
	return parseTailscaleIP(string(out))
}

// parseTailscaleIP takes the first non-blank line of `tailscale ip -4` and
// insists it is an address: a stopped tailscale prints a sentence there.
func parseTailscaleIP(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		ip := strings.TrimSpace(line)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("tailscale ip -4 printed %q, not an address: is tailscale up?", ip)
		}
		return ip, nil
	}
	return "", errors.New("tailscale ip -4 printed nothing: is tailscale up?")
}

// webAllow is what webGuard accepts as a request Host beyond IP literals
// and localhost: names under Suffix, and the bare ShortName — the first
// label of this node's own MagicDNS name, which a tailnet peer types
// because the suffix is a search domain. Either may be "" (MagicDNS off,
// or tailscale not running), which allows nothing extra.
type webAllow struct {
	Suffix    string
	ShortName string
}

// tailscaleStatusAllow asks the tailscale CLI for this node's MagicDNS
// suffix and this node's own DNS short name, so webGuard can recognise
// both a teammate's full node name (e.g.
// "michaelsmacbookpro2-q6uplpux.nodes.headscale.mage.net") and the bare
// short name a teammate types because the suffix is a DNS search domain
// (e.g. "michaels-claudes") as being on the tailnet. Bounded by a timeout
// for the same reason tailscaleIPv4 is: it runs at lobby start, before the
// TUI is up.
func tailscaleStatusAllow() (webAllow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return webAllow{}, fmt.Errorf("tailscale status --json: %w", err)
	}
	return parseTailscaleStatus(string(out))
}

// parseTailscaleStatus pulls MagicDNSSuffix and this node's own DNS short
// name (the first label of Self.DNSName) out of `tailscale status --json`.
// Both are optional — MagicDNS can be off, or Self.DNSName can be absent —
// in which case webGuard's host check falls back to IP literals and
// localhost only, the same as when tailscale is not running at all.
func parseTailscaleStatus(jsonOut string) (webAllow, error) {
	var v struct {
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
		Self           struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &v); err != nil {
		return webAllow{}, fmt.Errorf("tailscale status --json: %w", err)
	}
	allow := webAllow{Suffix: strings.ToLower(strings.Trim(v.MagicDNSSuffix, "."))}
	name := strings.ToLower(strings.Trim(v.Self.DNSName, "."))
	if name != "" {
		if i := strings.Index(name, "."); i >= 0 {
			allow.ShortName = name[:i]
		} else {
			allow.ShortName = name
		}
	}
	return allow, nil
}
