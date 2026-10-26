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

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

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
		say "Removed $UNIT_PATH"
	fi
	if [ -f "$SYSTEM_UNIT_PATH" ]; then
		as_root systemctl disable --now up >/dev/null 2>&1 || true
		as_root rm -f "$SYSTEM_UNIT_PATH"
		as_root systemctl daemon-reload
		say "Removed $SYSTEM_UNIT_PATH (system service)"
		say "Kept /var/lib/up (database), the up user and the up group. To copy the data after creating ~/.config/up:"
		say 'sudo cp /var/lib/up/up.db ~/.config/up/up.db && sudo chown $USER ~/.config/up/up.db'
	fi
	if [ -e "$INSTALL_DIR/up" ]; then
		in_dir "$INSTALL_DIR" rm -f "$INSTALL_DIR/up"
		say "Removed $INSTALL_DIR/up"
	fi
	say "Kept ~/.config/up (database)."
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
trap 'rm -rf "$TMP"' EXIT INT TERM

ARCHIVE="up_linux_${ARCH}.tar.gz"
say "Downloading $ARCHIVE from $BASE_URL"
curl -fsSL "$BASE_URL/$ARCHIVE" -o "$TMP/$ARCHIVE" || die "could not download $BASE_URL/$ARCHIVE"
curl -fsSL "$BASE_URL/checksums.txt" -o "$TMP/checksums.txt" || die "could not download $BASE_URL/checksums.txt"

grep " $ARCHIVE\$" "$TMP/checksums.txt" >"$TMP/expected" || die "$ARCHIVE is not listed in checksums.txt"
(cd "$TMP" && sha256sum -c expected >/dev/null 2>&1) || die "checksum mismatch for $ARCHIVE; nothing was installed"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" up || die "$ARCHIVE does not contain the up binary"

mkdir -p "$INSTALL_DIR" 2>/dev/null || as_root mkdir -p "$INSTALL_DIR"
in_dir "$INSTALL_DIR" install -m 0755 "$TMP/up" "$INSTALL_DIR/up"
say "Installed $INSTALL_DIR/up"

if [ "$SERVICE" -eq 1 ]; then
	command -v systemctl >/dev/null 2>&1 || die "--service needs systemd (systemctl not found); the binary is installed"
	if [ -f "$UNIT_PATH" ]; then
		say "Kept existing $UNIT_PATH (delete it and rerun to replace it)"
	else
		curl -fsSL "$BASE_URL/up.service" -o "$TMP/up.service" || die "could not download $BASE_URL/up.service"
		sed "s#/usr/local/bin/up#$INSTALL_DIR/up#" "$TMP/up.service" >"$TMP/up.service.out"
		mkdir -p "$(dirname "$UNIT_PATH")"
		install -m 0644 "$TMP/up.service.out" "$UNIT_PATH"
		say "Installed $UNIT_PATH"
	fi
	systemctl --user daemon-reload
	systemctl --user enable up >/dev/null 2>&1
	systemctl --user restart up
	if ! (as_root loginctl enable-linger "${USER:-$(id -un)}"); then
		say "To keep running after logout, run: sudo loginctl enable-linger ${USER:-$(id -un)}"
	fi
	say "Service up is running. Logs: journalctl --user -u up -f"
	say "Database: ~/.config/up/up.db (honors XDG_CONFIG_HOME). Open it with: up tui (no flags needed)."
elif [ "$SYSTEM" -eq 1 ]; then
	command -v systemctl >/dev/null 2>&1 || die "--system needs systemd (systemctl not found); the binary is installed"
	if ! id up >/dev/null 2>&1; then
		as_root useradd --system --user-group --no-create-home --shell "$(command -v nologin || echo /usr/sbin/nologin)" up
	fi
	if [ -f "$SYSTEM_UNIT_PATH" ]; then
		say "Kept existing $SYSTEM_UNIT_PATH (delete it and rerun to replace it)"
	else
		curl -fsSL "$BASE_URL/up-system.service" -o "$TMP/up-system.service" || die "could not download $BASE_URL/up-system.service"
		sed "s#/usr/local/bin/up#$INSTALL_DIR/up#" "$TMP/up-system.service" >"$TMP/up-system.service.out"
		as_root mkdir -p "$(dirname "$SYSTEM_UNIT_PATH")"
		as_root install -m 0644 "$TMP/up-system.service.out" "$SYSTEM_UNIT_PATH"
		say "Installed $SYSTEM_UNIT_PATH"
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
		say "Skipped group membership: running as root without a non-root SUDO_USER."
	fi
	say "Service up is running. Logs: journalctl -u up -f"
	say "Database: /var/lib/up/up.db. Open it with: up tui (no flags needed)."
	say "To use the TUI in this shell, run: newgrp up"
	say "or log out and back in. Until then: sudo -u up up tui"
	if [ -n "$target_user" ]; then
		target_home="$(getent passwd "$target_user" | cut -d: -f6)"
		if [ -n "$target_home" ] && [ -f "$target_home/.config/up/up.db" ]; then
			# Quote the path for a copyable shell command, including homes with apostrophes.
			old_db="$(printf '%s' "$target_home/.config/up/up.db" | sed "s/'/'\\\\''/g")"
			say "To use your existing database, run:"
			say "sudo cp '$old_db' /var/lib/up/up.db && sudo chown up:up /var/lib/up/up.db && sudo chmod 660 /var/lib/up/up.db && sudo systemctl restart up"
		fi
	fi
	if [ -f "$UNIT_PATH" ]; then
		say "Note: a user unit exists at $UNIT_PATH; the system database now takes precedence for up tui (unless --db or UP_DB is set)."
	fi
else
	say "Run it with: up serve   (add --service or --system to install it under systemd)"
fi
