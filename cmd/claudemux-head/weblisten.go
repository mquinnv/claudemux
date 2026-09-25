package main

import (
	"fmt"
	"net"
	"strconv"
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
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return webListen{}, false, fmt.Errorf("web.listen is %q: port must be a number from 1 to 65535", s)
	}
	return webListen{Host: host, Port: port, Tailscale: host == webTailscaleHost}, true, nil
}
