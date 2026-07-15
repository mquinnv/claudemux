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
amd64/arm64, packages each with the three bash scripts as **siblings**, and
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
