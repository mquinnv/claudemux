package main

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// shippedSkills are the Claude Code skills claudemux installs into
// ~/.claude/skills/. They are embedded in this binary rather than shipped as
// siblings like the hook scripts: a sibling would have to be added by hand to
// install.sh, release.yml AND the Homebrew formula's libexec list, and a
// `go install` of the head would never get it at all. Embedded, every install
// channel carries the skill that matches the launcher it was built with — which
// matters, since the skill documents claudemux's flags.
//
//go:embed skills
var shippedSkills embed.FS

// installSkills writes every shipped skill under claudeDir/skills/, replacing
// only files whose content differs. hook ensure runs on every launch, so an
// unconditional write would touch these files on every launch for nothing.
//
// claudemux owns the skill directories it ships (claudemux-*): a local edit to
// one is overwritten on the next launch. Nothing else under skills/ is touched.
func installSkills(claudeDir string) error {
	return fs.WalkDir(shippedSkills, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := shippedSkills.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(claudeDir, filepath.FromSlash(path))
		if have, err := os.ReadFile(dst); err == nil && bytes.Equal(have, want) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return writeAtomic(dst, want, 0o644)
	})
}

// writeAtomic writes blob to path via a temp file and a rename, so Claude
// Code scanning skills/ never reads a half-written SKILL.md.
func writeAtomic(path string, blob []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	// CreateTemp makes the file 0600; the mode has to be set before the
	// rename, since the destination inherits the temp file's.
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
