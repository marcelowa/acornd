#!/bin/sh
# Install acornd from a release: picks the binary for this machine,
# verifies its checksum, and runs preflight. Needs curl and sha256sum.
#
#   ACORND_GITHUB_TOKEN=... sh install.sh [--dir DIR] [--version TAG]
#
# While the repo is private the token is required (fine-grained,
# read-only, this repo); releases answer 404 without it. --dir defaults
# to ~/.acornd/bin, --version to the latest release.
set -eu

DIR="${ACORND_BIN_DIR:-$HOME/.acornd/bin}"
VERSION=""
TOKEN="${ACORND_GITHUB_TOKEN:-}"

while [ $# -gt 0 ]; do
	case "$1" in
		--dir) DIR="$2"; shift 2 ;;
		--version) VERSION="$2"; shift 2 ;;
		-h|--help) echo "usage: install.sh [--dir DIR] [--version TAG]"; exit 0 ;;
		*) echo "unknown flag $1"; exit 2 ;;
	esac
done

need() {
	command -v "$1" >/dev/null 2>&1 || { echo "install.sh: needs $1 on PATH"; exit 2; }
}
need curl
if command -v sha256sum >/dev/null 2>&1; then
	sum() { sha256sum "$1" | cut -d" " -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sum() { shasum -a 256 "$1" | cut -d" " -f1; }
else
	echo "install.sh: needs sha256sum or shasum on PATH"
	exit 2
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os/$arch" in
	linux/x86_64) asset="acornd-linux-amd64" ;;
	linux/aarch64|linux/arm64) asset="acornd-linux-arm64" ;;
	darwin/x86_64) asset="acornd-darwin-amd64" ;;
	darwin/aarch64|darwin/arm64) asset="acornd-darwin-arm64" ;;
	*) echo "install.sh: no release for $os/$arch"; exit 2 ;;
esac

api() {
	curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} -H "Accept: application/vnd.github+json" "$1"
}

# The tag: explicit, or the latest release. Compact the JSON first so one
# grep answers instead of a parser nobody guaranteed.
if [ -z "$VERSION" ]; then
	VERSION="$(api https://api.github.com/repos/marcelowa/acornd/releases/latest | tr -d '\n ' | grep -o '"tag_name":"[^"]*"' | head -n 1 | cut -d'"' -f4)"
	[ -n "$VERSION" ] || { echo "install.sh: no releases yet, or the token is missing"; exit 2; }
fi
echo "install.sh: release $VERSION, asset $asset"

release="$(api "https://api.github.com/repos/marcelowa/acornd/releases/tags/$VERSION" | tr -d '\n ')" || {
	echo "install.sh: cannot read release $VERSION (private repo needs ACORND_GITHUB_TOKEN)"
	exit 2
}
asset_id() {
	printf '%s' "$release" | grep -o "\"id\":[0-9]*,\"node_id\":\"[A-Za-z0-9_=-]*\",\"name\":\"$1\"" | head -n 1 | cut -d: -f2 | cut -d, -f1
}
gateway_id="$(asset_id "$asset")"
sums_id="$(asset_id sha256sums.txt)"
[ -n "$gateway_id" ] && [ -n "$sums_id" ] || { echo "install.sh: $VERSION holds no $asset"; exit 2; }

mkdir -p "$DIR"
get() {
	curl -fsSL ${TOKEN:+-H "Authorization: Bearer $TOKEN"} -H "Accept: application/octet-stream" \
		"https://api.github.com/repos/marcelowa/acornd/releases/assets/$1" -o "$2"
}
get "$gateway_id" "$DIR/acornd"
get "$sums_id" "$DIR/sha256sums.txt"

want="$(grep " $asset\$" "$DIR/sha256sums.txt" | cut -d" " -f1)"
[ -n "$want" ] || { echo "install.sh: checksum file names no $asset"; exit 2; }
# Names differ (acornd vs acornd-<os>-<arch>), hashes do not: compare.
[ "$(sum "$DIR/acornd")" = "$want" ] || { echo "install.sh: CHECKSUM MISMATCH, refusing"; exit 1; }
echo "install.sh: checksum OK"
chmod +x "$DIR/acornd"

echo "install.sh: preflight"
"$DIR/acornd" doctor || true
cat <<EOF
install.sh: next
  export ACORND_GITHUB_TOKEN='...'   # while the repo is private
  export PATH="\$PATH:$DIR"
  echo "\$OPENCODE_API_KEY" | acornd provider add opencode-go
  acornd start
EOF
