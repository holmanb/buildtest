#!/bin/sh
# Install Buildtest as a systemd user service.
#
# Builds the buildtest binary (if not already installed), installs it to
# ~/bin/buildtest, copies the user unit to ~/.config/systemd/user/, then
# enables and starts the service. The daemon listens on port 8000.
#
# Usage:
#   systemd/install.sh            # build and install from this repo
#   systemd/install.sh --dest-dir <dir>   # copy files into <dir> instead of
#                                         # installing (for packaging)
#
# Environment overrides:
#   BUILDTEST_BIN   existing binary to install instead of building (e.g. a
#                   downloaded release tarball)
#   DEST_DIR        same as --dest-dir
#   BIN_DIR         binary destination (default: ~/bin)
#   UNIT_DIR        unit destination (default: ~/.config/systemd/user)
#   HTTP_ADDR       address for --http (default: :8000)
set -e

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)

BIN_NAME=buildtest
SERVICE_NAME=${BIN_NAME}.service
HTTP_ADDR=${HTTP_ADDR:-:8000}

die() {
	echo "install: $*" >&2
	exit 1
}

usage() {
	echo "usage: $0 [--dest-dir <dir>]" >&2
	exit 2
}

DEST_DIR=${DEST_DIR:-}
while [ $# -gt 0 ]; do
	case "$1" in
	--dest-dir)
		[ $# -ge 2 ] || usage
		DEST_DIR=$2
		shift 2
		;;
	*)
		usage
		;;
	esac
done

# Locate or build the binary.
if [ -n "$BUILDTEST_BIN" ]; then
	[ -f "$BUILDTEST_BIN" ] || die "BUILDTEST_BIN not found: $BUILDTEST_BIN"
	SRC_BIN=$BUILDTEST_BIN
elif [ -f "$REPO_DIR/cmd/$BIN_NAME/$BIN_NAME" ]; then
	SRC_BIN=$REPO_DIR/cmd/$BIN_NAME/$BIN_NAME
else
	command -v go >/dev/null 2>&1 || die "go not found; build the binary first or set BUILDTEST_BIN"
	echo "Building $BIN_NAME..."
	(cd "$REPO_DIR" && go build -trimpath -o "cmd/$BIN_NAME/$BIN_NAME" "./cmd/$BIN_NAME")
	SRC_BIN=$REPO_DIR/cmd/$BIN_NAME/$BIN_NAME
fi

if [ -n "$DEST_DIR" ]; then
	# Staging mode for packaging: copy files into DEST_DIR and do nothing else.
	BIN_DIR=$DEST_DIR/bin
	UNIT_DIR=$DEST_DIR/.config/systemd/user
else
	[ -n "$HOME" ] || die "HOME is not set"
	BIN_DIR=${BIN_DIR:-$HOME/bin}
	UNIT_DIR=${UNIT_DIR:-$HOME/.config/systemd/user}
fi

mkdir -p "$BIN_DIR" "$UNIT_DIR"

# Install the binary.
install -m 0755 "$SRC_BIN" "$BIN_DIR/$BIN_NAME"

# Install the unit, substituting the HTTP address so the port can be changed
# without editing the unit by hand.
sed "s|--http :8000|--http $HTTP_ADDR|" \
	"$SCRIPT_DIR/user/$SERVICE_NAME" > "$UNIT_DIR/$SERVICE_NAME"

if [ -n "$DEST_DIR" ]; then
	echo "Staged files under $DEST_DIR:"
	ls -l "$BIN_DIR/$BIN_NAME" "$UNIT_DIR/$SERVICE_NAME"
	exit 0
fi

# Enable and start the service.
if ! systemctl --user daemon-reload; then
	die "cannot reach the systemd user manager; log in properly (not over plain ssh) and re-run, or start the service manually: systemctl --user enable --now $SERVICE_NAME"
fi
systemctl --user enable --now "$SERVICE_NAME"

echo "Installed $BIN_NAME to $BIN_DIR and enabled $SERVICE_NAME."
echo "The daemon is listening on $HTTP_ADDR (logs: journalctl --user -u $SERVICE_NAME)."
