#!/bin/sh
# Installs or updates rackwatch as a systemd service on TTY1, as described in SERVICE.md.
#
#   curl -fsSL https://raw.githubusercontent.com/m5lk3n/rackwatch/main/install.sh | sudo sh
#
# Takes the latest GitHub release, verifies its checksum and installs:
#   /opt/rackwatch/rackwatch                 the binary
#   /etc/rackwatch/config.toml               the config, from config.toml.example; never overwritten
#   /etc/systemd/system/rackwatch.service    the service, running as the rackwatch user
#
# Set RACKWATCH_VERSION (e.g. v0.1.0) to install a specific release instead.

set -eu

REPO="m5lk3n/rackwatch"
BIN_DIR="/opt/rackwatch"
CONFIG_DIR="/etc/rackwatch"
CONFIG="$CONFIG_DIR/config.toml"
UNIT="/etc/systemd/system/rackwatch.service"
SERVICE_USER="rackwatch"

say() { printf 'rackwatch: %s\n' "$*"; }
die() { printf 'rackwatch: error: %s\n' "$*" >&2; exit 1; }

# Everything runs from main, so a download cut off halfway executes nothing.
main() {
	[ "$(id -u)" -eq 0 ] || die "run as root, e.g. curl ... | sudo sh"
	[ "$(uname -s)" = Linux ] || die "rackwatch runs on Linux only"
	command -v systemctl >/dev/null || die "systemd is required"
	for cmd in curl tar sha256sum; do
		command -v "$cmd" >/dev/null || die "$cmd is required"
	done

	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	armv7l | armv8l) arch=armv7 ;;
	*) die "no release for architecture $(uname -m)" ;;
	esac

	# The latest release redirects to .../releases/tag/vX.Y.Z, which avoids
	# the API and its rate limit.
	tag="${RACKWATCH_VERSION:-}"
	if [ -z "$tag" ]; then
		url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
			die "couldn't look up the latest release"
		tag="${url##*/}"
	fi
	case "$tag" in
	v[0-9]*) ;;
	*) die "no release found (got '$tag')" ;;
	esac
	version="${tag#v}"
	dist="rackwatch_${version}_linux_${arch}"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	say "downloading $tag for linux/$arch"
	base="https://github.com/$REPO/releases/download/$tag"
	curl -fsSL -o "$tmp/$dist.tar.gz" "$base/$dist.tar.gz" || die "couldn't download $dist.tar.gz"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "couldn't download checksums.txt"
	(cd "$tmp" && grep " $dist.tar.gz\$" checksums.txt | sha256sum -c --quiet -) ||
		die "checksum mismatch for $dist.tar.gz"
	tar xzf "$tmp/$dist.tar.gz" -C "$tmp"

	if ! id "$SERVICE_USER" >/dev/null 2>&1; then
		say "creating system user $SERVICE_USER"
		nologin=$(command -v nologin || echo /usr/sbin/nologin)
		useradd --system --no-create-home --shell "$nologin" "$SERVICE_USER"
	fi

	install -d -m 755 "$BIN_DIR"
	install -m 755 "$tmp/$dist/rackwatch" "$BIN_DIR/rackwatch"
	say "installed $BIN_DIR/rackwatch ($version)"

	new_config=false
	if [ ! -e "$CONFIG" ]; then
		install -d -m 755 "$CONFIG_DIR"
		install -m 640 -o root -g "$SERVICE_USER" "$tmp/$dist/config.toml.example" "$CONFIG"
		new_config=true
		say "created $CONFIG from the example"
	else
		# It may hold the Telegram bot token, so only root and the service may read it.
		chown root:"$SERVICE_USER" "$CONFIG"
		chmod 640 "$CONFIG"
		say "kept existing $CONFIG"
	fi

	write_unit
	systemctl daemon-reload
	systemctl enable --quiet rackwatch.service
	# Not --now: that would end a login on TTY1, possibly this one.
	# Conflicts= in the unit stops the prompt once rackwatch starts.
	systemctl disable --quiet getty@tty1.service 2>/dev/null || true

	if [ "$new_config" = true ]; then
		say "edit $CONFIG, then start the dashboard with:"
		say "  sudo systemctl start rackwatch"
	elif [ "$(ps -o tty= -p $$ | tr -d ' ')" = tty1 ]; then
		say "not starting the dashboard from TTY1, as it would take over this login;"
		say "reboot, or run 'sudo systemctl restart rackwatch' over SSH"
	else
		systemctl restart rackwatch.service
		say "rackwatch is running on TTY1; check it with: systemctl status rackwatch"
	fi
}

write_unit() {
	# The input group lets the service read a touchscreen. A unit naming a
	# group that doesn't exist fails to start, so only add it where it does.
	groups=""
	if getent group input >/dev/null; then
		groups="SupplementaryGroups=input"
	fi
	cat >"$UNIT" <<EOF
[Unit]
Description=Rackwatch terminal dashboard
Wants=network-online.target
After=network-online.target
# Rackwatch and the login prompt can't share TTY1
Conflicts=getty@tty1.service
After=getty@tty1.service

[Service]
Type=simple
User=$SERVICE_USER
$groups
ExecStart=$BIN_DIR/rackwatch -config $CONFIG
Restart=always
RestartSec=2

# Attach the dashboard to the physical display's TTY
TTYPath=/dev/tty1
StandardInput=tty-force
StandardOutput=tty
StandardError=journal
TTYReset=yes
TTYVHangup=yes
TTYVTDisallocate=yes

Environment=TERM=linux

[Install]
WantedBy=multi-user.target
EOF
}

main "$@"
