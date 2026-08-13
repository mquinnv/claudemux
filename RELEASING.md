# Releasing claudemux

Cutting a new version touches two channels that consume one set of GitHub
Release artifacts. The order matters — publish the release **first**, because the
Homebrew formula points at release tarballs by URL, and a formula that ships
before its tarball exists fetches a 404.

Replace `X.Y.Z` throughout.

## 1. Tag and publish the release (produces the artifacts)

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

The `release` workflow cross-compiles `claudemux-head` for darwin/linux ×
amd64/arm64, packages each with the four bash scripts as **siblings**, and
publishes the tarballs plus `SHA256SUMS`. Wait for it:

```bash
gh run watch
gh release view vX.Y.Z --json assets --jq '.assets[].name'   # 4 tarballs + SHA256SUMS
```

`install.sh` needs nothing further — it always resolves the *latest* release.

## 2. Homebrew formula (in mquinnv/homebrew-tap)

The formula hardcodes the version and **all four** SHA256 sums, arch-mapped.
A swapped sum installs on your arch and fails for everyone on the other, so copy
carefully.

```bash
gh release download vX.Y.Z -R mquinnv/claudemux -p SHA256SUMS -O -
```

Edit `Formula/claudemux.rb`: bump `version`, replace each `url` and `sha256`.
The mapping is:

| SHA256SUMS line          | formula block          |
|--------------------------|------------------------|
| `..._darwin_arm64.tar.gz`| `on_macos` / `on_arm`  |
| `..._darwin_amd64.tar.gz`| `on_macos` / `on_intel`|
| `..._linux_arm64.tar.gz` | `on_linux`  / `on_arm` |
| `..._linux_amd64.tar.gz` | `on_linux`  / `on_intel`|

### The formula's `libexec.install` file list — check this EVERY release

The formula's `install` block has its own `libexec.install` line that names
every file it copies out of the tarball, independent of `install.sh` and
`.github/workflows/release.yml` in this repo. It is **not** generated from
those — it is a separate, hand-maintained list that must be kept in sync by
hand, every time a shipped script is added, renamed, or removed here.

As of this writing that list must name exactly these **six** files, kept as
siblings: `claudemux-head`, `claudemux`, `project-color-resolve.sh`,
`claudemux-map.sh`, `claudemux-worktree.sh`, `claudemux-ask.sh`.

This matters more than a typical packaging omission: `claudemux-head hook
ensure` resolves and validates every shipped script's source path **before
copying any of them** (see `cmd/claudemux-head/hook.go`). One missing
sibling — e.g. a formula that lists all but one of the files — doesn't just
fail to install the new script; it fails the entire hook-registration pass,
so the already-working `claudemux-map.sh` pane-map hook is silently lost too.
A formula that ships all but one of the files it needs is not "missing one
feature" — it is "hook registration is a total, silent no-op for every
Homebrew user."

If you added, renamed, or removed a shipped script in this release, update
**all three** places it is listed before tagging:

1. `install.sh` (this repo)
2. `.github/workflows/release.yml` (this repo)
3. `Formula/claudemux.rb`'s `libexec.install` list, in `mquinnv/homebrew-tap`
   (a separate repo — not edited from here)

Verify before pushing:

```bash
brew audit --strict --formula Formula/claudemux.rb   # must exit 0
brew install mquinnv/tap/claudemux                   # from the pushed tap
claudemux-head version                               # prints X.Y.Z
```

## Sanity check both channels

```bash
# each in a scratch HOME
brew install mquinnv/tap/claudemux
curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
```
