#!/bin/sh
# Install acornd from a release: picks the binary for this machine, verifies its checksum, prepares the
# machine as far as it can without a password, starts the console, and prints where to claim it.
#
#   sh install.sh [--dir DIR] [--version TAG] [--no-start] [--no-setup]
#
# --dir defaults to ~/.acornd/bin, --version to the latest release, and --no-start stops after the
# binary is in place. --no-setup leaves the machine alone, for an image that prepares it another way.
# Nothing here needs a token: the repository and its releases are public.
set -eu

DIR="${ACORND_BIN_DIR:-$HOME/.acornd/bin}"
VERSION=""
START=1
SETUP=1

while [ $# -gt 0 ]; do
	case "$1" in
		--dir) DIR="$2"; shift 2 ;;
		--version) VERSION="$2"; shift 2 ;;
		--no-start) START=0; shift ;;
		--no-setup) SETUP=0; shift ;;
		-h|--help) echo "usage: install.sh [--dir DIR] [--version TAG] [--no-start] [--no-setup]"; exit 0 ;;
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

# The tag: explicit, or wherever github.com sends a browser asking for the latest release. The
# redirect is read rather than api.github.com, because a machine that installs this once should not
# meet a rate limit for it.
if [ -z "$VERSION" ]; then
	VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
		https://github.com/marcelowa/acornd/releases/latest | sed 's#.*/tag/##')"
	[ -n "$VERSION" ] && [ "$VERSION" != "https://github.com/marcelowa/acornd/releases/latest" ] || {
		echo "install.sh: no releases yet, or github.com did not name one"; exit 2; }
fi
release="https://github.com/marcelowa/acornd/releases/download/$VERSION"
echo "install.sh: release $VERSION, asset $asset"

mkdir -p "$DIR"
get() { curl -fsSL "$1" -o "$2"; }
get "$release/$asset" "$DIR/acornd"
get "$release/sha256sums.txt" "$DIR/sha256sums.txt"

want="$(grep " $asset\$" "$DIR/sha256sums.txt" | cut -d" " -f1)"
[ -n "$want" ] || { echo "install.sh: checksum file names no $asset"; exit 2; }
# Names differ (acornd vs acornd-<os>-<arch>), hashes do not: compare.
[ "$(sum "$DIR/acornd")" = "$want" ] || { echo "install.sh: CHECKSUM MISMATCH, refusing"; exit 1; }
echo "install.sh: checksum OK"
chmod +x "$DIR/acornd"
BIN="$DIR/acornd"

# The machine first, and only as far as it can go without a password: setup runs the steps it can,
# prints the rest as commands, and never makes the install depend on this succeeding — a console that
# serves is more useful than one that refuses because bubblewrap is missing.
if [ "$SETUP" = "1" ]; then
	if [ "$(id -u)" = "0" ]; then
		"$BIN" setup --yes || echo "install.sh: setup did not finish; it printed what is left"
	else
		"$BIN" setup --yes || echo "install.sh: setup needs a terminal or sudo; run '$BIN setup' when you have one"
	fi
fi

if [ "$START" = "1" ]; then
	# Detached, because the person who ran this is about to close the terminal. Its own log, and the
	# setup URL it prints is the claim line on a fresh install.
	"$BIN" start -d || {
		echo "install.sh: the console did not start; run '$BIN start -d' and read its answer"
		exit 1
	}
fi

cat <<EOF

install.sh: next
  export PATH="\$PATH:$DIR"
  $( [ "$START" = "1" ] && echo "open the address above to claim this install" || echo "acornd start -d" )
EOF
