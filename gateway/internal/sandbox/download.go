package sandbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func Fetch(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, url, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: %s: %s", ErrUnavailable, url, response.Status)
	}
	return response.Body, nil
}

// UnpackVerified unpacks a release tarball, refusing to write anything that would land
// outside the destination. The archive is only as trustworthy as its checksum, which is why
// both checks happen: a verified download of a hostile archive would still be hostile.
func UnpackVerified(body io.Reader, want, dest string) error {
	digest := sha256.New()
	reader, err := gzip.NewReader(io.TeeReader(body, digest))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if err := extractEntry(archive, header, dest); err != nil {
			return err
		}
	}
	if got := hex.EncodeToString(digest.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("%w: checksum mismatch: got %s, want %s", ErrUnavailable, got, want)
	}
	return nil
}

// extractEntry writes one archive member. Directories, files and symlinks are the three shapes a
// release takes; anything else is skipped rather than guessed at.
func extractEntry(archive *tar.Reader, header *tar.Header, dest string) error {
	target, err := safeJoin(dest, header.Name)
	if err != nil {
		return err
	}
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		return extractFile(archive, header, target)
	case tar.TypeSymlink:
		return extractSymlink(header, target)
	}
	return nil
}

func extractFile(archive *tar.Reader, header *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// The owner-write bit is added because an archive member read-only in the release would
	// otherwise leave a file this install cannot replace on the next upgrade.
	mode := header.FileInfo().Mode().Perm() | 0o200
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, archive); err != nil {
		// The copy's error is the one to report; the close of a file already known to be bad
		// would only say the same thing twice.
		_ = file.Close()
		return err
	}
	return file.Close()
}

func extractSymlink(header *tar.Header, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// The link's own destination has to stay inside the tree too: a symlink to /etc is how an
	// archive reaches a path that the file entries cannot.
	if _, err := safeJoin(filepath.Dir(target), header.Linkname); err != nil {
		return err
	}
	// A later layer replaces an earlier one, and the removal is allowed to find nothing there.
	_ = os.Remove(target)
	return os.Symlink(header.Linkname, target)
}

// safeJoin resolves an archive member inside a destination, refusing absolute paths and
// escapes. A tar entry named ../../.ssh/authorized_keys is otherwise a way to write outside.
func safeJoin(dest, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: archive member %q is absolute", ErrUnavailable, name)
	}
	target := filepath.Join(dest, name)
	relative, err := filepath.Rel(dest, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: archive member %q escapes the destination", ErrUnavailable, name)
	}
	return target, nil
}

// FirstLine keeps one engine complaint rather than a wall of them: the first is the cause,
// the rest are usually consequences.
func FirstLine(text string) string {
	if before, _, ok := strings.Cut(text, "\n"); ok {
		return before
	}
	return text
}
