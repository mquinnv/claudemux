#!/usr/bin/env node
// Fetch the claudemux release tarball for this platform into vendor/.
// The four files must land as SIBLINGS: claudemux resolves
// project-color-resolve.sh, and claudemux-head resolves claudemux-map.sh, by
// looking next to their own path.
const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

const REPO = "mquinnv/claudemux";
const version = require("./package.json").version;

const platform = { darwin: "darwin", linux: "linux" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
if (!platform || !arch) {
  console.error(
    `claudemux: unsupported platform ${process.platform}/${process.arch}. ` +
      `It needs tmux and bash; Windows is not supported.`,
  );
  process.exit(1);
}

const tarball = `claudemux_${version}_${platform}_${arch}.tar.gz`;
const url = `https://github.com/${REPO}/releases/download/v${version}/${tarball}`;
const vendor = path.join(__dirname, "vendor");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "claudemux-"));

try {
  fs.mkdirSync(vendor, { recursive: true });
  execFileSync("curl", ["-fsSL", url, "-o", path.join(tmp, tarball)], { stdio: "inherit" });
  execFileSync("tar", ["-xzf", path.join(tmp, tarball), "-C", vendor], { stdio: "inherit" });
  for (const f of ["claudemux-head", "claudemux", "project-color-resolve.sh", "claudemux-map.sh"]) {
    fs.chmodSync(path.join(vendor, f), 0o755);
  }
} catch (err) {
  console.error(`claudemux: install failed: ${err.message}`);
  console.error(`claudemux: you can install manually from https://github.com/${REPO}`);
  process.exit(1);
} finally {
  fs.rmSync(tmp, { recursive: true, force: true });
}

// Register the Claude Code hook — best-effort, OUTSIDE the try above and never
// fatal. `hook ensure` can legitimately exit non-zero (e.g. a settings.json that
// does not parse), and the binaries are already installed by this point, so a
// hook hiccup must not fail `npm install`. The launcher re-registers at startup
// anyway, so this is only a convenience.
try {
  execFileSync(path.join(vendor, "claudemux-head"), ["hook", "ensure"], { stdio: "inherit" });
} catch {
  console.error(
    "claudemux: could not register the Claude Code hook now; it will be " +
      "registered the first time you run `claudemux`.",
  );
}
