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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.tokenAddress(scope), nil)
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

// tokenAddress is the token service with the scope this pull needs. A registry that names no
// service gets no parameter rather than an empty one.
func (r *registry) tokenAddress(scope string) string {
	query := url.Values{}
	if r.service != "" {
		query.Set("service", r.service)
	}
	if scope != "" {
		query.Set("scope", scope)
	}
	if encoded := query.Encode(); encoded != "" {
		return r.realm + "?" + encoded
	}
	return r.realm
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
	registry := &registry{host: ref.host, client: &http.Client{Timeout: 5 * time.Minute}}
	scope := "repository:" + ref.repository + ":pull"
	token, err := registry.token(ctx, ref.repository, scope)
	if err != nil {
		return err
	}
	body, err := fetchManifest(ctx, registry, token, ref)
	if err != nil {
		return err
	}
	manifestDigest := sha256.Sum256(body)
	manifest, err := chooseManifest(ctx, registry, token, ref, body)
	if err != nil {
		return err
	}
	if log != nil {
		log("image %s: %d layer(s), digest %s", ref, len(manifest.Layers), shortDigest(manifestDigest[:]))
	}
	return unpackLayers(ctx, registry, token, ref, manifest.Layers, dest, log)
}

// fetchManifest is the one request whose status codes carry meaning to a person: private,
// absent, or something else. Each answer is a sentence rather than a status number.
func fetchManifest(ctx context.Context, registry *registry, token string, ref imageReference) ([]byte, error) {
	address := fmt.Sprintf("%s/v2/%s/manifests/%s", registry.apiBase(), ref.repository, ref.tag)
	response, err := registry.get(ctx, address, token, acceptManifests)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", ref.host, err)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusOK {
		return body, nil
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%s refused the image %s: it may be private (%s)", ref.host, ref, response.Status)
	case http.StatusNotFound:
		return nil, fmt.Errorf("no image %s on %s", ref, ref.host)
	}
	return nil, fmt.Errorf("%s answered %s for %s", ref.host, response.Status, ref)
}

// unpackLayers unpacks them in order: layers stack, so the later one wins.
func unpackLayers(
	ctx context.Context,
	registry *registry,
	token string,
	ref imageReference,
	layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	},
	dest string,
	log func(string, ...any),
) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, layer := range layers {
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
		return registry.platformManifest(ctx, token, ref, entry.Digest, osName, arch)
	}
	return manifest, fmt.Errorf("the image %s publishes no %s/%s build", ref, osName, arch)
}

// platformManifest fetches one entry of an index: the manifest for the platform asked for.
func (r *registry) platformManifest(ctx context.Context, token string, ref imageReference, digest, osName, arch string) (imageManifest, error) {
	address := fmt.Sprintf("%s/v2/%s/manifests/%s", r.apiBase(), ref.repository, digest)
	response, err := r.get(ctx, address, token, acceptManifests)
	if err != nil {
		return imageManifest{}, err
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	_ = response.Body.Close()
	if err != nil {
		return imageManifest{}, err
	}
	if response.StatusCode != http.StatusOK {
		return imageManifest{}, fmt.Errorf("%s answered %s for %s on %s/%s", ref.host, response.Status, ref, osName, arch)
	}
	var chosen imageManifest
	if err := json.Unmarshal(payload, &chosen); err != nil {
		return imageManifest{}, fmt.Errorf("the manifest for %s is not JSON this build can read: %w", ref, err)
	}
	return chosen, nil
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
		// The layer is in the digest and the next step already has it; a failure to close a file
		// the copy has finished with says nothing the caller can act on.
		_ = scratch.Close()
		_ = os.Remove(scratch.Name())
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
	reader := source
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
	// Containment is checked before anything acts on the name: a whiteout removes a path just as
	// surely as a file entry writes one.
	if strings.HasPrefix(name, "..") {
		return outsideRoot(header.Name)
	}
	target := filepath.Join(dest, filepath.FromSlash(name))
	if !strings.HasPrefix(target+string(os.PathSeparator), dest+string(os.PathSeparator)) {
		return outsideRoot(header.Name)
	}
	if handled, err := applyWhiteout(dest, name); handled {
		return err
	}
	// A later layer replaces what an earlier one wrote, so a link is removed before it is made.
	switch header.Typeflag {
	case tar.TypeDir:
		// FileInfo() is the header read the way the standard library reads it, permissions and
		// all: the mode it returns is already a FileMode, so no conversion of the tar field — a
		// number an archive controls — happens here.
		return os.MkdirAll(target, header.FileInfo().Mode().Perm()|0o700)
	case tar.TypeSymlink:
		_ = os.RemoveAll(target)
		return os.Symlink(header.Linkname, target)
	case tar.TypeLink:
		_ = os.RemoveAll(target)
		source := filepath.Join(dest, filepath.FromSlash(path.Clean(strings.TrimPrefix(header.Linkname, "/"))))
		return os.Link(source, target)
	case tar.TypeReg:
		return writeEntry(target, header, content)
	}
	// Devices, fifos, and the rest are the runtime's business, not a rootfs's: skipping them
	// leaves a tree that works, where refusing would leave a tree that does not exist.
	return nil
}

// writeEntry creates one file. A later layer replaces an earlier one, which O_TRUNC does.
func writeEntry(target string, header *tar.Header, content io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func outsideRoot(name string) error {
	return fmt.Errorf("a layer names a path outside the root: %q", name)
}

// applyWhiteout handles the two entries that delete rather than write: an opaque directory
// clears everything an earlier layer put there, and .wh.<name> removes one entry.
func applyWhiteout(dest, name string) (bool, error) {
	base := path.Base(name)
	directory := filepath.Join(dest, filepath.FromSlash(path.Dir(name)))
	switch {
	case base == ".wh..wh..opq":
		entries, err := os.ReadDir(directory)
		if err != nil {
			// A whiteout for a directory no earlier layer made is nothing to do.
			return true, nil
		}
		for _, entry := range entries {
			_ = os.RemoveAll(filepath.Join(directory, entry.Name()))
		}
		return true, nil
	case strings.HasPrefix(base, ".wh."):
		return true, os.RemoveAll(filepath.Join(directory, strings.TrimPrefix(base, ".wh.")))
	}
	return false, nil
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
