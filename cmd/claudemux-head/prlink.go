package main

import (
	"fmt"
	"strconv"
	"strings"
)

// PRLink is a pull request a session linked. Claude Code records one as a
// `pr-link` transcript record when the session opens a PR (or is pointed at
// one), carrying prNumber, prRepository ("owner/repo") and prUrl.
type PRLink struct {
	Number int
	Repo   string
	URL    string
}

// lastPRLink returns the newest PR a batch of events links, or prev when the
// batch links none. A session that opens a second PR moves on to it.
func lastPRLink(events []Event, prev PRLink) PRLink {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].PR.Number > 0 {
			return events[i].PR
		}
	}
	return prev
}

// infoPROption carries the session's active PR to the lobby as
// "owner/repo#N", "" when the session has linked none. The URL is not
// published: prURL derives it from the repo and number, and the lobby's
// snapshot parser is tab-delimited, so the shorter the value the better.
const infoPROption = "@claudemux_pr"

// prOptionValue renders a PRLink as the @claudemux_pr value.
func prOptionValue(pr PRLink) string {
	if pr.Number <= 0 {
		return ""
	}
	return sanitizeOptionValue(fmt.Sprintf("%s#%d", pr.Repo, pr.Number))
}

// parsePROption splits an @claudemux_pr value into repo and number. ok is
// false for an empty or malformed value, which renders as no PR.
func parsePROption(v string) (repo string, num int, ok bool) {
	i := strings.LastIndexByte(v, '#')
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(v[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return v[:i], n, true
}

// prURL is the GitHub URL of the PR named by repo and number, "" when the repo
// is unknown.
func prURL(repo string, num int) string {
	if repo == "" || num <= 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", repo, num)
}
