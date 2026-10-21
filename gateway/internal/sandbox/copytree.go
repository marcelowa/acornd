package sandbox

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyTree copies a directory tree, keeping what an image depends on: modes, symlinks, and
// executability. It is how a workspace gets its own writable copy of a shared unpacked image,
// and why an install inside the sandbox is an ordinary write.
//
// Hard links become separate files, which costs space only where an image used them — the same
// trade a plain `cp -r` makes, and the reason this needs no platform-specific stat.
func copyTree(source, dest string) error {
	// The walk skips the root entry, so the destination is made here; `/bin` is a symlink into
	// `/usr` in a Debian image, and a symlink needs somewhere to be.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(dest, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info)
		}
		return nil
	})
}

// copyFile writes one file with its mode.
func copyFile(source, dest string, info fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
