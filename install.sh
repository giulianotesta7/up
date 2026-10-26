#!/bin/sh
# Installs the up binary from a GitHub release (linux amd64/arm64).
#
#   curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --service
#
# Options:
#   --service         install, enable and start a systemd user service
#                     (database in ~/.config/up/up.db)
#   --system          install a shared system service (database in /var/lib/up)
#   --version TAG     install a specific release, for example v1.2.0
#   --uninstall       remove the binary and the unit; keep the database
#   -h, --help        show this help
#
# Environment:
#   UP_INSTALL_DIR    where the binary goes (default /usr/local/bin)
#   UP_BASE_URL       directory URL holding the release files (testing/mirrors)
#   UP_SYSTEM_UNIT_PATH system unit path (default /etc/systemd/system/up.service)
set -eu

REPO="giulianotesta7/up"
INSTALL_DIR="${UP_INSTALL_DIR:-/usr/local/bin}"
UNIT_PATH="${UP_UNIT_PATH:-${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/up.service}"
SYSTEM_UNIT_PATH="${UP_SYSTEM_UNIT_PATH:-${UP_LEGACY_UNIT_PATH:-/etc/systemd/system/up.service}}"
SERVICE=0
SYSTEM=0
UNINSTALL=0
VERSION=""

BOLD='' GREEN='' YELLOW='' RED='' RESET='' ERROR_RED='' ERROR_RESET=''
CHECK='+'
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != dumb ]; then
	BOLD='\033[1m' GREEN='\033[32m' YELLOW='\033[33m' RED='\033[31m' RESET='\033[0m'
	case "${LC_ALL:-} ${LC_CTYPE:-} ${LANG:-}" in
	*[Uu][Tt][Ff]-8* | *[Uu][Tt][Ff]8*) CHECK='✓' ;;
	esac
	if [ -t 2 ]; then ERROR_RED="$RED"; ERROR_RESET="$RESET"; fi
fi
say() { printf '%s\n' "$*"; }
step() { printf '%b> %s%b\n' "$BOLD" "$*" "$RESET"; }
ok() { printf '%b%s %s%b\n' "$GREEN" "$CHECK" "$*" "$RESET"; }
warn() { printf '%b! %s%b\n' "$YELLOW" "$*" "$RESET"; }
cmd() { printf '%b%s%b\n' "$BOLD" "$*" "$RESET"; }
heading() { printf '%b%s%b\n' "$BOLD" "$*" "$RESET"; }
DONE=0
DIED=0
SUDO_NOTICED=0
TMP=''
# Keep notices visible even when an as_root command silences its own output.
exec 3>&1
die() {
	DIED=1
	printf '%bx install.sh: %s%b\n' "$ERROR_RED" "$*" "$ERROR_RESET" >&2
	exit 1
}
finish() {
	status=$?
	if [ "$status" -ne 0 ] && [ "$DONE" -eq 0 ] && [ "$DIED" -eq 0 ]; then
		printf '%bx install.sh failed. Nothing was left half-installed except what is listed above.%b\n' "$ERROR_RED" "$ERROR_RESET" >&2
	fi
	if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
	exit "$status"
}
trap finish EXIT
trap 'if [ -n "$TMP" ]; then rm -rf "$TMP"; fi' INT TERM

usage() {
	cat <<'EOF'
Usage: install.sh [--service | --system] [--version TAG] [--uninstall]

Installs the up binary from a GitHub release (linux amd64/arm64).

  --service         install, enable and start a systemd user service
                    (database in ~/.config/up/up.db); run as a normal user
  --system          install, enable and start a shared system service
                    (database in /var/lib/up/up.db); may run as root
  --version TAG     install a specific release, for example v1.2.0
  --uninstall       remove the binary and user/system units; keep databases and up user/group
  -h, --help        show this help

Environment:
  UP_INSTALL_DIR    where the binary goes (default /usr/local/bin)
  UP_BASE_URL       directory URL holding the release files (testing/mirrors)
  UP_UNIT_PATH      user unit path (default $XDG_CONFIG_HOME/systemd/user/up.service,
                    or ~/.config/systemd/user/up.service)
  UP_SYSTEM_UNIT_PATH system unit path (default /etc/systemd/system/up.service)
  UP_LEGACY_UNIT_PATH compatibility alias for the system unit path
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--service) SERVICE=1 ;;
	--system) SYSTEM=1 ;;
	--uninstall) UNINSTALL=1 ;;
	--version)
		[ $# -ge 2 ] || die "--version needs a tag, for example v1.2.0"
		VERSION="$2"
		shift
		;;
	-h | --help)
		usage
		DONE=1
		exit 0
		;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
	shift
done

if [ "$SERVICE" -eq 1 ] && [ "$SYSTEM" -eq 1 ]; then
	die "--system and --service are mutually exclusive"
fi

# as_root runs a command as root, through sudo when needed.
as_root() {
	if [ "$(id -u)" -eq 0 ]; then
		"$@"
	elif command -v sudo >/dev/null 2>&1; then
		if [ "$SUDO_NOTICED" -eq 0 ]; then
			step "Using sudo for: $*; you may be asked for your password." >&3
			SUDO_NOTICED=1
		fi
		sudo "$@"
	else
		die "root privileges are needed for: $* (run as root or install sudo)"
	fi
}

# in_dir runs a command as the current user when the directory is
# writable, otherwise as root.
in_dir() {
	dir="$1"
	shift
	if [ -w "$dir" ]; then "$@"; else as_root "$@"; fi
}

if [ "$UNINSTALL" -eq 1 ]; then
	if [ -f "$UNIT_PATH" ]; then
		systemctl --user disable --now up >/dev/null 2>&1 || true
		rm -f "$UNIT_PATH"
		systemctl --user daemon-reload
		ok "Removed $UNIT_PATH"
	fi
	if [ -f "$SYSTEM_UNIT_PATH" ]; then
		as_root systemctl disable --now up >/dev/null 2>&1 || true
		as_root rm -f "$SYSTEM_UNIT_PATH"
		as_root systemctl daemon-reload
		ok "Removed $SYSTEM_UNIT_PATH (system service)"
		warn "Kept /var/lib/up (database), the up user and the up group. To copy the data after creating ~/.config/up:"
		cmd 'sudo cp /var/lib/up/up.db ~/.config/up/up.db && sudo chown $USER ~/.config/up/up.db'
	fi
	if [ -e "$INSTALL_DIR/up" ]; then
		in_dir "$INSTALL_DIR" rm -f "$INSTALL_DIR/up"
		ok "Removed $INSTALL_DIR/up"
	fi
	warn "Kept ~/.config/up (database)."
	DONE=1
	exit 0
fi

if [ "$SERVICE" -eq 1 ] && [ "$(id -u)" -eq 0 ]; then
	die "--service cannot run as root: user services belong to a normal user"
fi

[ "$(uname -s)" = "Linux" ] || die "only Linux is supported (found $(uname -s))"
case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $(uname -m) (supported: amd64, arm64)" ;;
esac
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"

if [ -n "${UP_BASE_URL:-}" ]; then
	BASE_URL="${UP_BASE_URL%/}"
elif [ -n "$VERSION" ]; then
	BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
else
	BASE_URL="https://github.com/$REPO/releases/latest/download"
fi

TMP="$(mktemp -d)"

ARCHIVE="up_linux_${ARCH}.tar.gz"
step "Installing up (${VERSION:-latest}, linux/$ARCH) to $INSTALL_DIR"
if [ -n "${UP_BASE_URL:-}" ]; then say "  Source: $BASE_URL"; fi
curl -fsSL "$BASE_URL/$ARCHIVE" -o "$TMP/$ARCHIVE" 2>/dev/null || die "could not download $BASE_URL/$ARCHIVE"
ok "Downloaded $ARCHIVE"
curl -fsSL "$BASE_URL/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null || die "could not download $BASE_URL/checksums.txt"

grep " $ARCHIVE\$" "$TMP/checksums.txt" >"$TMP/expected" || die "$ARCHIVE is not listed in checksums.txt"
(cd "$TMP" && sha256sum -c expected >/dev/null 2>&1) || die "checksum mismatch for $ARCHIVE; nothing was installed"
ok "Verified checksum"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" up || die "$ARCHIVE does not contain the up binary"

mkdir -p "$INSTALL_DIR" 2>/dev/null || as_root mkdir -p "$INSTALL_DIR"
in_dir "$INSTALL_DIR" install -m 0755 "$TMP/up" "$INSTALL_DIR/up"
ok "Installed $INSTALL_DIR/up"

if [ "$SERVICE" -eq 1 ]; then
	step "Setting up the user service"
	command -v systemctl >/dev/null 2>&1 || die "--service needs systemd (systemctl not found); the binary is installed"
	if [ -f "$UNIT_PATH" ]; then
		warn "Kept existing $UNIT_PATH (delete it and rerun to replace it)"
	else
		curl -fsSL "$BASE_URL/up.service" -o "$TMP/up.service" 2>/dev/null || die "could not download $BASE_URL/up.service"
		sed "s#/usr/local/bin/up#$INSTALL_DIR/up#" "$TMP/up.service" >"$TMP/up.service.out"
		mkdir -p "$(dirname "$UNIT_PATH")"
		install -m 0644 "$TMP/up.service.out" "$UNIT_PATH"
		ok "Installed $UNIT_PATH"
	fi
	systemctl --user daemon-reload
	systemctl --user enable up >/dev/null 2>&1
	systemctl --user restart up
	if ! (as_root loginctl enable-linger "${USER:-$(id -un)}"); then
		warn "To keep running after logout, run:"
		cmd "sudo loginctl enable-linger ${USER:-$(id -un)}"
	fi
	ok "Service started"
	say ""
	heading "Next steps"
	cmd "  1. up tui"
	cmd "  Logs      journalctl --user -u up -f"
	say "  Database  ~/.config/up/up.db (honors XDG_CONFIG_HOME)"
elif [ "$SYSTEM" -eq 1 ]; then
	step "Setting up the system service"
	command -v systemctl >/dev/null 2>&1 || die "--system needs systemd (systemctl not found); the binary is installed"
	if ! id up >/dev/null 2>&1; then
		as_root useradd --system --user-group --no-create-home --shell "$(command -v nologin || echo /usr/sbin/nologin)" up
		ok "Created user and group up"
	else
		ok "User up already exists"
	fi
	if [ -f "$SYSTEM_UNIT_PATH" ]; then
		warn "Kept existing $SYSTEM_UNIT_PATH (delete it and rerun to replace it)"
	else
		curl -fsSL "$BASE_URL/up-system.service" -o "$TMP/up-system.service" 2>/dev/null || die "could not download $BASE_URL/up-system.service"
		sed "s#/usr/local/bin/up#$INSTALL_DIR/up#" "$TMP/up-system.service" >"$TMP/up-system.service.out"
		as_root mkdir -p "$(dirname "$SYSTEM_UNIT_PATH")"
		as_root install -m 0644 "$TMP/up-system.service.out" "$SYSTEM_UNIT_PATH"
		ok "Installed $SYSTEM_UNIT_PATH"
	fi
	as_root systemctl daemon-reload
	as_root systemctl enable up >/dev/null 2>&1
	as_root systemctl restart up
	target_user=""
	if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
		target_user="$SUDO_USER"
	elif [ "$(id -u)" -ne 0 ]; then
		target_user="$(id -un)"
	fi
	if [ -n "$target_user" ]; then
		as_root usermod -aG up "$target_user"
	else
		warn "Skipped group membership: running as root without a non-root SUDO_USER."
	fi
	ok "Service started"
	say ""
	heading "Next steps"
	cmd "  1. newgrp up"
	say "     (or log out and back in)"
	cmd "  2. up tui"
	cmd "     Until then: sudo -u up up tui"
	cmd "  Logs      journalctl -u up -f"
	say "  Database  /var/lib/up/up.db"
	if [ -n "$target_user" ]; then
		target_home="$(getent passwd "$target_user" | cut -d: -f6)"
		if [ -n "$target_home" ] && [ -f "$target_home/.config/up/up.db" ]; then
			# Quote the path for a copyable shell command, including homes with apostrophes.
			old_db="$(printf '%s' "$target_home/.config/up/up.db" | sed "s/'/'\\\\''/g")"
			say ""
			say "Found your existing database. To use it in the shared service, run:"
			cmd "  sudo cp '$old_db' /var/lib/up/up.db && sudo chown up:up /var/lib/up/up.db && sudo chmod 660 /var/lib/up/up.db && sudo systemctl restart up"
		fi
	fi
	if [ -f "$UNIT_PATH" ]; then
		say ""
		warn "Note: a user unit exists at $UNIT_PATH; the system database now takes precedence for up tui (unless --db or UP_DB is set)."
	fi
else
	say ""
	heading "Next steps"
	cmd "  1. up serve"
	cmd "  2. up tui"
	say "  Add --service or --system to install a daemon under systemd."
fi
DONE=1
