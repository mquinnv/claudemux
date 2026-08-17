package main

import (
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// bannerFixture is a fully-populated card: a session the lobby knows everything
// about, which is the common case for a conducted arrival.
func bannerFixture() bannerCard {
	return bannerCard{
		Session: "remix-2",
		Topic:   "Fix idle detection",
		Color:   "b34dff",
		Model:   "claude-opus-4-7",
		Context: 42,
	}
}

// captionOf returns the lines below the locomotive — everything the card says
// about the session.
func captionOf(t *testing.T, lines []string) []string {
	t.Helper()
	if len(lines) < len(swBannerTrain) {
		t.Fatalf("card has %d lines, fewer than the %d-line train: %q", len(lines), len(swBannerTrain), lines)
	}
	return lines[len(swBannerTrain):]
}

func TestRenderBannerCardShowsSessionTopicAndStats(t *testing.T) {
	caption := captionOf(t, renderBannerCard(bannerFixture()))
	if len(caption) != 3 {
		t.Fatalf("want topic, name and stats lines, got %d: %q", len(caption), caption)
	}
	if !strings.Contains(caption[0], "Fix idle detection") {
		t.Errorf("first caption line must lead with the topic, got %q", caption[0])
	}
	if !strings.Contains(caption[0], strings.TrimSpace(swBannerGutter)) {
		t.Errorf("the topic must carry its emphasis gutter, got %q", caption[0])
	}
	if !strings.Contains(caption[1], "remix-2") {
		t.Errorf("caption must name the session, got %q", caption[1])
	}
	if !strings.Contains(caption[1], "escorted by claudemux") {
		t.Errorf("caption must say who moved the client, got %q", caption[1])
	}
	if !strings.Contains(caption[2], "opus 4.7") {
		t.Errorf("stats line must name the model, got %q", caption[2])
	}
	if !strings.Contains(caption[2], "42% context") {
		t.Errorf("stats line must report the context, got %q", caption[2])
	}
}

// A head that has published nothing yet still gets a card — only a barer one.
// The name line is the floor, never omitted.
func TestRenderBannerCardOmitsUnpublishedFacts(t *testing.T) {
	caption := captionOf(t, renderBannerCard(bannerCard{Session: "api", Context: -1}))
	if len(caption) != 1 {
		t.Fatalf("want the name line alone, got %d: %q", len(caption), caption)
	}
	if !strings.Contains(caption[0], "api") {
		t.Errorf("name line = %q, want the session name", caption[0])
	}
}

// Half-published is the interesting middle: a model but no context (a head that
// has not sampled one yet) must not render "-1% context".
func TestRenderBannerCardStatsDropUnknownContext(t *testing.T) {
	caption := captionOf(t, renderBannerCard(bannerCard{Session: "api", Model: "claude-haiku-4-5", Context: -1}))
	if len(caption) != 2 {
		t.Fatalf("want name and stats lines, got %d: %q", len(caption), caption)
	}
	if strings.Contains(caption[1], "-1") || strings.Contains(caption[1], "context") {
		t.Errorf("unknown context must not reach the stats line, got %q", caption[1])
	}
	if !strings.Contains(caption[1], "haiku 4.5") {
		t.Errorf("stats line = %q, want the model", caption[1])
	}
}

// A topic of only whitespace is a topic the head never really set; it must not
// buy a line of its own (and certainly not a bare gutter bar).
func TestRenderBannerCardIgnoresBlankTopic(t *testing.T) {
	caption := captionOf(t, renderBannerCard(bannerCard{Session: "api", Topic: "   ", Context: -1}))
	if len(caption) != 1 {
		t.Fatalf("blank topic bought a line: %q", caption)
	}
}

// The locomotive is fixed art: only its centering indent may vary with the
// card's contents, never the drawing itself.
func TestRenderBannerCardTrainIsFixedArt(t *testing.T) {
	base := renderBannerCard(bannerFixture())
	cards := []bannerCard{
		{Session: "a", Context: -1},
		{Session: strings.Repeat("x", 200), Topic: strings.Repeat("t", 200), Context: 100},
		bannerFixture(),
	}
	for _, c := range cards {
		lines := renderBannerCard(c)
		for i := range swBannerTrain {
			if strings.TrimLeft(lines[i], " ") != strings.TrimLeft(base[i], " ") {
				t.Errorf("%q: train line %d changed with the card: %q vs %q",
					c.Session, i, lines[i], base[i])
			}
		}
	}
}

func TestRenderBannerCardTruncatesLongFields(t *testing.T) {
	lines := renderBannerCard(bannerCard{
		Session: strings.Repeat("x", 200),
		Topic:   strings.Repeat("t", 200),
		Model:   strings.Repeat("m", 200),
		Context: 100,
	})
	for i, l := range lines {
		if w := lipgloss.Width(l); w > swBannerMaxW {
			t.Errorf("line %d is %d cells, exceeds cap %d", i, w, swBannerMaxW)
		}
	}
}

func TestSwBannerAccentFallsBackOnJunk(t *testing.T) {
	if got, want := swBannerAccent("b34dff"), lipgloss.Color("#b34dff"); got != want {
		t.Errorf("swBannerAccent(hex) = %v, want %v", got, want)
	}
	// A tmux user option can hold anything; anything but a bare 6-digit hex
	// would emit a broken escape if passed through.
	for _, junk := range []string{"", "purple", "#b34dff", "b34df", "zzzzzz"} {
		if got, want := swBannerAccent(junk), lipgloss.Color(swBannerDefaultAccent); got != want {
			t.Errorf("swBannerAccent(%q) = %v, want the default %v", junk, got, want)
		}
	}
}

func TestSwWantsBanner(t *testing.T) {
	cases := []struct {
		target, lobby string
		want          bool
	}{
		{"remix-2", "claudemux-switch", true},
		{"claudemux-switch", "claudemux-switch", false}, // returning home needs no announcement
		{"", "claudemux-switch", false},
	}
	for _, c := range cases {
		if got := swWantsBanner(c.target, c.lobby); got != c.want {
			t.Errorf("swWantsBanner(%q, %q) = %v, want %v", c.target, c.lobby, got, c.want)
		}
	}
}

func TestBannerForCarriesTheSnapshotsFacts(t *testing.T) {
	m := swModel{snap: swSnapshot{
		Lobby: "switchboard",
		Sessions: []swSession{{
			Name: "remix-2", Topic: "Fix idle detection", Color: "b34dff",
			Model: "claude-opus-4-7", Context: 42, Summary: "reading banner.go",
		}},
	}}
	card := m.bannerFor("remix-2")
	if card == nil {
		t.Fatal("bannerFor returned no card for a real session")
	}
	if *card != bannerFixture() {
		t.Errorf("bannerFor = %+v, want %+v", *card, bannerFixture())
	}
}

// A head that has not named its tab yet has usually still published what it is
// doing right now, and that beats a blank topic line.
func TestBannerForFallsBackToSummary(t *testing.T) {
	m := swModel{snap: swSnapshot{
		Lobby:    "switchboard",
		Sessions: []swSession{{Name: "api", Summary: "reading banner.go", Context: -1}},
	}}
	card := m.bannerFor("api")
	if card == nil {
		t.Fatal("bannerFor returned no card")
	}
	if card.Topic != "reading banner.go" {
		t.Errorf("Topic = %q, want the summary", card.Topic)
	}
}

// A session that raced away between the conductor's decision and the switch is
// still worth announcing: its name is the one fact that is certainly true.
func TestBannerForUnknownSessionKeepsTheName(t *testing.T) {
	m := swModel{snap: swSnapshot{Lobby: "switchboard"}}
	card := m.bannerFor("vanished")
	if card == nil {
		t.Fatal("bannerFor returned no card for an unknown session")
	}
	if want := (bannerCard{Session: "vanished", Context: -1}); *card != want {
		t.Errorf("bannerFor = %+v, want %+v", *card, want)
	}
}

func TestBannerForSkipsMovesThatNeedNoAnnouncement(t *testing.T) {
	m := swModel{snap: swSnapshot{Lobby: "switchboard"}}
	for _, target := range []string{"switchboard", ""} {
		if card := m.bannerFor(target); card != nil {
			t.Errorf("bannerFor(%q) = %+v, want no card", target, *card)
		}
	}
}

func TestSwBannerCommandPassesFactsAsFlags(t *testing.T) {
	got := swBannerCommand("/usr/local/bin/claudemux-head", bannerFixture())
	want := "'/usr/local/bin/claudemux-head' banner " +
		"--topic 'Fix idle detection' --color 'b34dff' --model 'claude-opus-4-7' " +
		"--context 42 'remix-2'"
	if got != want {
		t.Errorf("swBannerCommand =\n  %q\nwant\n  %q", got, want)
	}
}

// Flags must precede the session name — Go's flag package stops parsing at the
// first positional argument, so a trailing flag would be silently ignored.
func TestSwBannerCommandPutsSessionLast(t *testing.T) {
	cmd := swBannerCommand("/bin/self", bannerFixture())
	if !strings.HasSuffix(cmd, " 'remix-2'") {
		t.Errorf("session must be the last word, got %q", cmd)
	}
}

func TestSwBannerCommandOmitsUnpublishedFacts(t *testing.T) {
	got := swBannerCommand("/bin/self", bannerCard{Session: "api", Context: -1})
	if want := "'/bin/self' banner 'api'"; got != want {
		t.Errorf("swBannerCommand = %q, want %q", got, want)
	}
}

// A zero context is a real reading, not a missing one.
func TestSwBannerCommandKeepsZeroContext(t *testing.T) {
	if got := swBannerCommand("/bin/self", bannerCard{Session: "api"}); !strings.Contains(got, "--context 0") {
		t.Errorf("swBannerCommand = %q, want a --context 0 flag", got)
	}
}

func TestSwBannerPopupArgsTargetsClientAndQuotesCommand(t *testing.T) {
	args := swBannerPopupArgs("/usr/local/bin/claudemux-head", "client-0", bannerCard{Session: "it's", Context: -1})
	if args[0] != "display-popup" {
		t.Fatalf("want display-popup argv, got %q", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c client-0") {
		t.Errorf("popup must target the driven client, got %q", joined)
	}
	shellCmd := args[len(args)-1]
	if want := `'/usr/local/bin/claudemux-head' banner 'it'\''s'`; shellCmd != want {
		t.Errorf("shell command = %q, want %q", shellCmd, want)
	}
}

func TestSwBannerPopupArgsSizesToCard(t *testing.T) {
	card := bannerFixture()
	args := swBannerPopupArgs("/bin/self", "c", card)
	var w, h string
	for i, a := range args {
		if a == "-w" && i+1 < len(args) {
			w = args[i+1]
		}
		if a == "-h" && i+1 < len(args) {
			h = args[i+1]
		}
	}
	lines := renderBannerCard(card)
	// Content lines + 2 border rows.
	if want := strconv.Itoa(len(lines) + 2); h != want {
		t.Errorf("popup height = %q, want content+border = %q", h, want)
	}
	widest := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > widest {
			widest = lw
		}
	}
	if want := strconv.Itoa(widest + 2); w != want {
		t.Errorf("popup width = %q, want card+border = %q", w, want)
	}
}

func TestSwShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got, want := swShellQuote("it's"), `'it'\''s'`; got != want {
		t.Errorf("swShellQuote = %q, want %q", got, want)
	}
}

// The flags the lobby writes must round-trip back into the card it described.
func TestParseBannerArgsRoundTripsSwBannerCommand(t *testing.T) {
	args := []string{"--topic", "Fix idle detection", "--color", "b34dff",
		"--model", "claude-opus-4-7", "--context", "42", "remix-2"}
	got, ok := parseBannerArgs(args, io.Discard)
	if !ok {
		t.Fatal("parseBannerArgs rejected the lobby's own flags")
	}
	if got != bannerFixture() {
		t.Errorf("parseBannerArgs = %+v, want %+v", got, bannerFixture())
	}
}

// A lobby running an older binary invokes the popup with the session name
// alone. That must still parse, with every unpublished fact left unset.
func TestParseBannerArgsAcceptsBareSessionName(t *testing.T) {
	got, ok := parseBannerArgs([]string{"api"}, io.Discard)
	if !ok {
		t.Fatal("parseBannerArgs rejected a bare session name")
	}
	if want := (bannerCard{Session: "api", Context: -1}); got != want {
		t.Errorf("parseBannerArgs = %+v, want %+v", got, want)
	}
}

func TestParseBannerArgsRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"--nope", "api"}} {
		if _, ok := parseBannerArgs(args, io.Discard); ok {
			t.Errorf("parseBannerArgs(%q) accepted bad usage", args)
		}
	}
}

func TestRunBannerRejectsBadUsage(t *testing.T) {
	var out, errOut strings.Builder
	if rc := runBanner(nil, &out, &errOut); rc != 2 {
		t.Errorf("runBanner(nil) = %d, want 2", rc)
	}
	if out.Len() != 0 {
		t.Errorf("runBanner wrote a card to stdout on a usage error: %q", out.String())
	}
}

func TestRunBannerOutputFitsPopupViewport(t *testing.T) {
	var out strings.Builder
	if rc := runBanner([]string{"--topic", "Fix idle detection", "api"}, &out, io.Discard); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	s := out.String()
	if !strings.HasPrefix(s, "\x1b[?25l") {
		t.Error("banner must hide the cursor for the popup's lifetime")
	}
	if !strings.HasSuffix(s, "\x1b[?25h") {
		t.Error("banner must restore the cursor before exiting")
	}
	body := strings.TrimPrefix(strings.TrimSuffix(s, "\x1b[?25h"), "\x1b[?25l")
	for _, want := range []string{"Fix idle detection", "api"} {
		if !strings.Contains(body, want) {
			t.Errorf("card is missing %q: %q", want, body)
		}
	}
	if strings.HasSuffix(body, "\n") {
		t.Error("a trailing newline scrolls the card and parks the cursor on a blank bottom row")
	}
	want := len(renderBannerCard(bannerCard{Session: "api", Topic: "Fix idle detection", Context: -1})) - 1
	if got := strings.Count(body, "\n"); got != want {
		t.Errorf("body has %d newlines, want %d (N lines joined, not terminated)", got, want)
	}
}
