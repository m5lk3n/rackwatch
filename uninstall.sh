#!/bin/sh
# Removes rackwatch as installed by install.sh and gives TTY1 back to the login prompt.
#
#   curl -fsSL https://raw.githubusercontent.com/m5lk3n/rackwatch/main/uninstall.sh | sudo sh
#
# Removes:
#   /etc/systemd/system/rackwatch.service    the service
#   /opt/rackwatch/                          the binary
#
# Keeps /etc/rackwatch/config.toml and the rackwatch user, so a later install
# picks up where this one left off. To remove those as well, purge:
#
#   curl -fsSL https://raw.githubusercontent.com/m5lk3n/rackwatch/main/uninstall.sh | sudo sh -s -- --purge
#
# Setting RACKWATCH_PURGE=1 does the same.

set -eu

BIN_DIR="/opt/rackwatch"
CONFIG_DIR="/etc/rackwatch"
UNIT="/etc/systemd/system/rackwatch.service"
SERVICE_USER="rackwatch"

say() { printf 'rackwatch: %s\n' "$*"; }
die() { printf 'rackwatch: error: %s\n' "$*" >&2; exit 1; }

# Everything runs from main, so a download cut off halfway executes nothing.
main() {
	purge="${RACKWATCH_PURGE:-0}"
	for arg in "$@"; do
		case "$arg" in
		--purge) purge=1 ;;
		*) die "unknown option '$arg' (only --purge is supported)" ;;
		esac
	done

	[ "$(id -u)" -eq 0 ] || die "run as root, e.g. curl ... | sudo sh"
	command -v systemctl >/dev/null || die "systemd is required"

	if [ -e "$UNIT" ]; then
		systemctl disable --now --quiet rackwatch.service 2>/dev/null || true
		rm -f "$UNIT"
		systemctl daemon-reload
		systemctl reset-failed rackwatch.service 2>/dev/null || true
		say "removed the service"
	else
		say "no service at $UNIT, skipping"
	fi

	# Rackwatch may have been stopped before restoring the console's colors.
	# This clears TTY1, so it runs before the login prompt comes back.
	setterm --reset >/dev/tty1 2>/dev/null || true
	# install.sh disabled the login prompt on TTY1; bring it back.
	systemctl enable --quiet getty@tty1.service 2>/dev/null || true
	systemctl start getty@tty1.service 2>/dev/null ||
		say "couldn't start the login prompt on TTY1; it returns after a reboot"

	if [ -e "$BIN_DIR" ]; then
		rm -rf "$BIN_DIR"
		say "removed $BIN_DIR"
	fi

	if [ "$purge" = 1 ]; then
		if [ -e "$CONFIG_DIR" ]; then
			rm -rf "$CONFIG_DIR"
			say "removed $CONFIG_DIR"
		fi
		if id "$SERVICE_USER" >/dev/null 2>&1; then
			userdel "$SERVICE_USER"
			say "removed system user $SERVICE_USER"
		fi
		# userdel leaves the group behind where user private groups are off.
		if getent group "$SERVICE_USER" >/dev/null 2>&1; then
			groupdel "$SERVICE_USER" 2>/dev/null || true
		fi
	else
		[ -e "$CONFIG_DIR" ] && say "kept $CONFIG_DIR"
		id "$SERVICE_USER" >/dev/null 2>&1 && say "kept system user $SERVICE_USER"
		say "to remove those as well, run again with --purge"
	fi

	say "rackwatch is uninstalled"
}

main "$@"
