# claudemux

This is the npm distribution of [claudemux](https://github.com/mquinnv/claudemux), a
tmux workspace for Claude Code sessions with a live status pane.

Installing this package (`npm i -g claudemux` or `npx claudemux`) does not ship any
JavaScript logic itself — a `postinstall` script downloads the prebuilt `claudemux` and
`claudemux-head` binaries (plus their sibling scripts) for your platform from the
[GitHub releases page](https://github.com/mquinnv/claudemux/releases) and registers the
Claude Code hook.

For documentation, usage, and other install methods (Homebrew, `curl | sh`), see the
main repository: https://github.com/mquinnv/claudemux
