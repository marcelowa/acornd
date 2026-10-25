package sandbox

import (
	"io"
	"io/fs"
	"os"
	"path"
)

// copyTree copies a directory tree, keeping what an image depends on: modes, symlinks, and
// executability. It is how a workspace gets its own writable copy of a shared unpacked image,
// and why an install inside the sandbox is an ordinary write.
//
// Both trees are opened as roots, so every read and every write is confined to the tree it
// names. That matters because the tree being copied came from a registry: a link that points
// outside the image is reproduced as a link and never followed, and a link swapped in while the
// copy runs cannot redirect a write out of the destination. A walk over plain paths can promise
// neither, because the check and the use are two different steps.
//
// Hard links become separate files, which costs space only where an image used them — the same
// trade a plain `cp -r` makes, and the reason this needs no platform-specific stat.
func copyTree(source, dest string) error {
	// The walk skips the root entry, so the destination is made here; `/bin` is a symlink into
	// `/usr` in a Debian image, and a symlink needs somewhere to be.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	in, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	// Names from here on are slash-separated and relative to the source, which is the shape both
	// roots and fs.FS want: `bin/sh`, never an absolute path.
	return fs.WalkDir(in.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		// The walk's root is the source folder itself, which the destination already stands in for.
		if err != nil || name == "." {
			return err
		}
		return copyOne(in, out, name, entry)
	})
}

// copyOne is one entry of the walk: a folder with its own mode, a link reproduced as a link, or a
// file copied whole. A device or a fifo has no meaning in a rootfs a workspace can write to, so it
// is left out rather than refused.
func copyOne(in, out *os.Root, name string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	switch {
	case entry.IsDir():
		return out.MkdirAll(name, info.Mode().Perm()|0o700)
	case info.Mode()&os.ModeSymlink != 0:
		link, err := in.Readlink(name)
		if err != nil {
			return err
		}
		// Replaced whole: a later copy of the same tree writes over an earlier one, and a link is
		// removed before it is made rather than written through.
		_ = out.RemoveAll(name)
		return out.Symlink(link, name)
	case info.Mode().IsRegular():
		return copyFile(in, out, name, info)
	}
	return nil
}

// copyFile writes one file with its mode. Both ends are roots, so the name it is handed is
// resolved inside the two trees the caller opened and nowhere else.
func copyFile(in, out *os.Root, name string, info fs.FileInfo) error {
	if err := out.MkdirAll(path.Dir(name), 0o755); err != nil {
		return err
	}
	source, err := in.Open(name)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := out.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dest, source); err != nil {
		// The copy's error is the one to report; the close on a file already known to be bad
		// would only say the same thing twice.
		_ = dest.Close()
		return err
	}
	return dest.Close()
}
