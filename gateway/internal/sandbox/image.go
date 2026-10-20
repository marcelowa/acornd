package sandbox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pulling an image into a plain directory, so an engine with no runtime can still start a
// workspace from one.
//
// It is deliberately the small half of what a container runtime does: fetch the manifest for
// this platform, verify and unpack its layers into a folder, and cache the result by the
// manifest's own digest. No store, no reference counting, no snapshots — a workspace copies the
// cached tree and owns that copy. That copy is what makes an install inside the sandbox outlive
// the command that made it: the workspace's rootfs is its own disk, and the host never sees a
// byte of it.

// imageSource fetches and unpacks an image reference into dest, which it creates. It is a field
// on the driver so that a test can answer without a registry.
type imageSource func(ctx context.Context, reference, dest string, log func(string, ...any)) error

// registry is the part of the distribution API this needs: a token, a manifest, and blobs.
type registry struct {
	host   string
	client *http.Client
	// realm and service come from the registry's own challenge, so the same code works for
	// Docker Hub and for a mirror.
	realm   string
	service string
}

// imageReference is a repository and a tag, with the registry it lives on.
type imageReference struct {
	host, repository, tag string
}

// String is the reference as a person writes it, for messages.
func (r imageReference) String() string { return r.host + "/" + r.repository + ":" + r.tag }

// parseImageReference reads `debian:stable-slim`, `acme/tool:1.2`, and `ghcr.io/acme/tool:1.2`.
// Docker Hub's official images live under `library/`, and a bare name means that registry.
func parseImageReference(value string) (imageReference, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return imageReference{}, errors.New("no image named")
	}
	ref := imageReference{host: "docker.io", tag: "latest"}
	rest := trimmed
	if first, after, ok := strings.Cut(trimmed, "/"); ok && (strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		ref.host, rest = first, after
	}
	if name, tag, ok := strings.Cut(rest, ":"); ok {
		rest, ref.tag = name, tag
	}
	if ref.tag == "" {
		return imageReference{}, fmt.Errorf("%q has an empty tag", value)
	}
	if rest == "" {
		return imageReference{}, fmt.Errorf("%q names no image", value)
	}
	if !strings.Contains(rest, "/") {
		rest = "library/" + rest
	}
	ref.repository = rest
	return ref, nil
}

// platform is what this binary can run: an image's other platforms are not usable here.
func platform() (string, string) { return "linux", runtime.GOARCH }

// apiBase is where the distribution API lives for this registry. Docker Hub answers v2 on a
// different host than its auth service, which is why the challenge is read rather than assumed.
func (r *registry) apiBase() string {
	if r.host == "docker.io" {
		return "https://registry-1.docker.io"
	}
	// A registry on this machine speaks plain HTTP unless it says otherwise, the way docker and
	// podman treat an insecure local registry. Everything else is HTTPS: an image is code, and
	// fetching it in the clear is not a default worth having.
	name, _, _ := strings.Cut(r.host, ":")
	if name == "localhost" || name == "127.0.0.1" || name == "::1" {
		return "http://" + r.host
	}
	return "https://" + r.host
}

// authorize asks the registry what it wants and follows the challenge. Anonymous pulls are
// what a public image needs; a private one gets the registry's own refusal, in words.
func (r *registry) authorize(ctx context.Context, repository string) error {
	if r.realm != "" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBase()+"/v2/", nil)
	if err != nil {
		return err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("the registry %s did not answer: %w", r.host, err)
	}
	defer response.Body.Close()
	challenge := response.Header.Get("WWW-Authenticate")
	if challenge == "" {
		return nil
	}
	params := parseChallenge(challenge)
	r.realm = params["realm"]
	r.service = params["service"]
	if r.realm == "" {
		return fmt.Errorf("the registry %s answered with a challenge this build cannot read: %s", r.host, challenge)
	}
	_ = repository
	return nil
}

// parseChallenge reads a `Bearer realm="…",service="…"` header without pretending to be a full
// parser: that shape is the only one the distribution API uses. The scheme word in front is not
// a parameter, so it is removed first.
func parseChallenge(header string) map[string]string {
	params := map[string]string{}
	if _, rest, ok := strings.Cut(header, " "); ok {
		header = rest
	}
	for _, part := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return params
}

// token asks for a pull token. A registry that needs none (a mirror, or an anonymous public
// one) answers with an unauthenticated request, and this returns "" for it.
func (r *registry) token(ctx context.Context, repository, scope string) (string, error) {
	if err := r.authorize(ctx, repository); err != nil {
		return "", err
	}
	if r.realm == "" {
		return "", nil
	}
	query := url.Values{}
	if r.service != "" {
		query.Set("service", r.service)
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	address := r.realm
	if encoded := query.Encode(); encoded != "" {
		address += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("the registry's token service did not answer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry's token service said %s", response.Status)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("the registry's token service answered something this build cannot read: %w", err)
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	return payload.AccessToken, nil
}

// get is an authorized request against the distribution API.
func (r *registry) get(ctx context.Context, address, token string, accept []string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for _, value := range accept {
		request.Header.Add("Accept", value)
	}
	return r.client.Do(request)
}

// acceptManifests is what this build can read: an index to pick a platform from, or the
// manifest itself.
var acceptManifests = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

// pullImage fetches an image and unpacks its layers into dest. Every layer is verified against
// the digest the manifest names before it is unpacked: an image is code, and code that arrives
// unverified is not worth running.
func pullImage(ctx context.Context, reference, dest string, log func(string, ...any)) error {
	ref, err := parseImageReference(reference)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	registry := &registry{host: ref.host, client: client}
	scope := "repository:" + ref.repository + ":pull"
	token, err := registry.token(ctx, ref.repository, scope)
	if err != nil {
		return err
	}

	// The manifest: an index on a multi-platform image, a manifest on a single-platform one.
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", registry.apiBase(), ref.repository, ref.tag)
	response, err := registry.get(ctx, manifestURL, token, acceptManifests)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", ref.host, err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%s refused the image %s: it may be private (%s)", ref.host, ref, response.Status)
		}
		if response.StatusCode == http.StatusNotFound {
			return fmt.Errorf("no image %s on %s", ref, ref.host)
		}
		return fmt.Errorf("%s answered %s for %s", ref.host, response.Status, ref)
	}
	manifestDigest := sha256.Sum256(body)
	manifest, err := chooseManifest(ctx, registry, token, ref, body)
	if err != nil {
		return err
	}
	if log != nil {
		log("image %s: %d layer(s), digest %s", ref, len(manifest.Layers), shortDigest(manifestDigest[:]))
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, layer := range manifest.Layers {
		if err := applyLayer(ctx, registry, token, ref, layer, dest, log); err != nil {
			return err
		}
	}
	return nil
}

// imageManifest is the part of a manifest this needs: the layers, in order.
type imageManifest struct {
	MediaType string `json:"mediaType"`
	Layers    []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Platform  struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// chooseManifest picks this machine's manifest out of an index, and is the manifest itself when
// the image publishes one platform.
func chooseManifest(ctx context.Context, registry *registry, token string, ref imageReference, body []byte) (imageManifest, error) {
	var manifest imageManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return manifest, fmt.Errorf("the manifest for %s is not JSON this build can read: %w", ref, err)
	}
	if len(manifest.Manifests) == 0 {
		if len(manifest.Layers) == 0 {
			return manifest, fmt.Errorf("the manifest for %s has no layers", ref)
		}
		return manifest, nil
	}
	osName, arch := platform()
	for _, entry := range manifest.Manifests {
		// An attestation manifest carries no platform of its own; asking for ours skips it.
		if entry.Platform.OS != osName || entry.Platform.Architecture != arch {
			continue
		}
		url := fmt.Sprintf("%s/v2/%s/manifests/%s", registry.apiBase(), ref.repository, entry.Digest)
		response, err := registry.get(ctx, url, token, acceptManifests)
		if err != nil {
			return manifest, err
		}
		payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if err != nil {
			return manifest, err
		}
		if response.StatusCode != http.StatusOK {
			return manifest, fmt.Errorf("%s answered %s for %s on %s/%s", ref.host, response.Status, ref, osName, arch)
		}
		var chosen imageManifest
		if err := json.Unmarshal(payload, &chosen); err != nil {
			return manifest, fmt.Errorf("the manifest for %s is not JSON this build can read: %w", ref, err)
		}
		return chosen, nil
	}
	return manifest, fmt.Errorf("the image %s publishes no %s/%s build", ref, osName, arch)
}

// applyLayer downloads one layer, checks its digest, and unpacks it in order. The digest is
// checked against the manifest before a byte reaches the filesystem, so a corrupted download is
// a refusal rather than a half-unpacked root.
func applyLayer(ctx context.Context, registry *registry, token string, ref imageReference, layer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}, dest string, log func(string, ...any)) error {
	address := fmt.Sprintf("%s/v2/%s/blobs/%s", registry.apiBase(), ref.repository, layer.Digest)
	response, err := registry.get(ctx, address, token, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s for a layer of %s", ref.host, response.Status, ref)
	}
	scratch, err := os.CreateTemp(dest, ".layer-*")
	if err != nil {
		return err
	}
	defer func() {
		scratch.Close()
		os.Remove(scratch.Name())
	}()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(scratch, digest), response.Body)
	if err != nil {
		return fmt.Errorf("reading a layer of %s: %w", ref, err)
	}
	if got := "sha256:" + hex.EncodeToString(digest.Sum(nil)); got != layer.Digest {
		return fmt.Errorf("a layer of %s did not match its digest: the manifest says %s and the bytes are %s", ref, layer.Digest, got)
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if log != nil {
		log("image %s: layer %s (%d bytes) verified", ref, shortDigest([]byte(layer.Digest)), written)
	}
	return unpackLayer(scratch, layer.MediaType, dest)
}

// unpackLayer writes one layer's entries into dest. Layers stack, so a later one replaces an
// earlier one; a whiteout removes what a later layer deleted.
func unpackLayer(source io.Reader, mediaType, dest string) error {
	var reader io.Reader = source
	// A layer is gzip unless the manifest says otherwise; zstd is refused rather than
	// half-read, because silently unpacking nothing would look like a broken image.
	switch {
	case strings.Contains(mediaType, "zstd"):
		return errors.New("this image's layers are zstd-compressed, which this build does not unpack yet")
	case strings.Contains(mediaType, "gzip"), mediaType == "":
		gzipReader, err := gzip.NewReader(source)
		if err != nil {
			return fmt.Errorf("a layer is not gzip: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading a layer: %w", err)
		}
		if err := applyEntry(dest, header, archive); err != nil {
			return err
		}
	}
}

// applyEntry is one tar entry: a directory, a file, a link, or a whiteout. Paths are cleaned
// and kept inside dest, so a layer cannot write outside the root it is unpacking.
func applyEntry(dest string, header *tar.Header, content io.Reader) error {
	name := path.Clean(strings.TrimPrefix(header.Name, "/"))
	if name == "." || name == "" {
		return nil
	}
	if strings.HasPrefix(name, "..") {
		return fmt.Errorf("a layer names a path outside the root: %q", header.Name)
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	if !strings.HasPrefix(target+string(os.PathSeparator), dest+string(os.PathSeparator)) {
		return fmt.Errorf("a layer names a path outside the root: %q", header.Name)
	}
	base := path.Base(name)
	directory := path.Dir(name)
	switch {
	case base == ".wh..wh..opq":
		// An opaque directory: everything the earlier layers put here is gone.
		inside := filepath.Join(dest, filepath.FromSlash(directory))
		entries, err := os.ReadDir(inside)
		if err != nil {
			return nil
		}
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(inside, entry.Name()))
		}
		return nil
	case strings.HasPrefix(base, ".wh."):
		// A whiteout: the file or folder was deleted between layers.
		return os.RemoveAll(filepath.Join(dest, filepath.FromSlash(directory), strings.TrimPrefix(base, ".wh.")))
	}
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, os.FileMode(header.Mode)&os.ModePerm|0o700)
	case tar.TypeSymlink:
		_ = os.RemoveAll(target)
		return os.Symlink(header.Linkname, target)
	case tar.TypeLink:
		_ = os.RemoveAll(target)
		source := filepath.Join(dest, filepath.FromSlash(path.Clean(strings.TrimPrefix(header.Linkname, "/"))))
		return os.Link(source, target)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&os.ModePerm)
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, content); err != nil {
			file.Close()
			return err
		}
		return file.Close()
	}
	// Devices, fifos, and the rest are the runtime's business, not a rootfs's: skipping them
	// leaves a tree that works, where refusing would leave a tree that does not exist.
	return nil
}

// shortDigest keeps a log line readable: the first twelve characters of the hex.
func shortDigest(digest []byte) string {
	text := hex.EncodeToString(digest)
	text = strings.TrimPrefix(text, "sha256:")
	if len(text) > 12 {
		return text[:12]
	}
	return text
}
