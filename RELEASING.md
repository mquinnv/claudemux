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
amd64/arm64, packages each with the five bash scripts as **siblings**, and
publishes the tarballs plus `SHA256SUMS`. Wait for it:

```bash
gh run watch
gh release view vX.Y.Z --json assets --jq '.assets[].name'   # 4 tarballs + SHA256SUMS
```

Then confirm the tarball actually contains every sibling — this is the cheapest
place to catch a `release.yml` that forgot a newly added script:

```bash
gh release download vX.Y.Z -R mquinnv/claudemux -p 'claudemux_X.Y.Z_darwin_arm64.tar.gz'
tar -tzf claudemux_X.Y.Z_darwin_arm64.tar.gz    # 6 shipped files + LICENSE + README.md
```

`install.sh` needs nothing further — it always resolves the *latest* release.

## 2. Homebrew formula (in mquinnv/homebrew-tap)

The formula hardcodes **all four** SHA256 sums, arch-mapped. A swapped sum
installs on your arch and fails for everyone on the other, so copy carefully.

It does **not** carry a `version` line. Homebrew scans the version out of the
release-tarball URL, and an explicit `version` is now rejected by
`brew audit --strict` as redundant. Don't add one back; bumping the four URLs
bumps the version.

```bash
gh release download vX.Y.Z -R mquinnv/claudemux -p SHA256SUMS -O -
```

Edit `Formula/claudemux.rb`: replace each `url` and `sha256`. The mapping is:

| SHA256SUMS line          | formula block          |
|--------------------------|------------------------|
| `..._darwin_arm64.tar.gz`| `on_macos` / `on_arm`  |
| `..._darwin_amd64.tar.gz`| `on_macos` / `on_intel`|
| `..._linux_arm64.tar.gz` | `on_linux`  / `on_arm` |
| `..._linux_amd64.tar.gz` | `on_linux`  / `on_intel`|

Better than reading that table across four blocks by eye: key each sum off the
tarball filename already present in the `url` line next to it, so a transposition
is impossible rather than merely unlikely. The sums are only ever wrong when a
human moves them between blocks.

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

The two-repo split is what makes this bite. `claudemux-worktree.sh` and
`claudemux-ask.sh` were added to `install.sh` and `release.yml` during the
1.1.0 -> 1.2.0 window; the formula's list was correct for 1.1.0 and simply
could not be updated in the same commit, because it lives in another repo. So
the formula is *always* one release behind by construction, and the only thing
standing between that and a silently broken hook install is someone checking
this list at bump time. Check it at bump time.

If you added, renamed, or removed a shipped script in this release, update
**all three** places it is listed before tagging:

1. `install.sh` (this repo)
2. `.github/workflows/release.yml` (this repo)
3. `Formula/claudemux.rb`'s `libexec.install` list, in `mquinnv/homebrew-tap`
   (a separate repo — not edited from here)

### Verify before pushing

`brew audit` takes a **formula name, not a path** — `brew audit <path>` is
disabled outright. Auditing by name reads the *tapped* copy, so overlay your
edit into the local tap first, audit there, then push:

```bash
tapdir="$(brew --repository)/Library/Taps/mquinnv/homebrew-tap"
cp Formula/claudemux.rb "$tapdir/Formula/claudemux.rb"

brew audit --strict --online mquinnv/tap/claudemux   # must exit 0
brew fetch mquinnv/tap/claudemux                     # url + sha256 resolve for THIS arch
brew info mquinnv/tap/claudemux | head -1            # prints "stable X.Y.Z"
```

`brew fetch` is the non-destructive half of an install check: it proves the URL
resolves and the sum matches without linking anything into the prefix. It only
covers the arch you're on — the other three sums are protected by keying them
off the filenames, above.

Once the audit is green, push the tap, then resync the local copy so `brew
update` isn't sitting on a dirty tree:

```bash
git -C "$tapdir" checkout -- Formula/claudemux.rb
git -C "$tapdir" pull --ff-only
```

## Sanity check both channels

```bash
brew install mquinnv/tap/claudemux
claudemux-head version                               # prints X.Y.Z

# a scratch HOME *does* isolate this one: install.sh writes to
# ${CLAUDEMUX_PREFIX:-$HOME/.local/bin}
curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
```

The two halves isolate differently, so don't treat them the same. `install.sh`
is confined by `HOME`; `brew install` links into the Homebrew prefix globally
and a scratch `HOME` does nothing to contain it. On a machine where you also
run claudemux from source (`~/.local/bin`, `~/go/bin`), the Homebrew symlinks
and your dev build will shadow each other depending on `PATH` order. And if any
live session *is* running the Homebrew build, `binwatch` will notice its own
binary change underneath it and re-exec the head (see
`cmd/claudemux-head/binwatch.go`, which names a release upgrade as exactly this
case), so a verification install is not observationally free.

Do the Homebrew half somewhere you don't develop. If you can't, `brew fetch`
plus the `tar -tzf` listing in step 1 already cover what actually breaks
(bad sum, bad URL, missing sibling); skip the install and say so rather than
quietly claiming a check you didn't run.

## Tell people to upgrade

`brew upgrade claudemux` alone compares against cached tap metadata and will
report nothing to do. The refresh is the part that matters:

```bash
brew update && brew upgrade claudemux
```
