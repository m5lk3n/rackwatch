# Service

*I can put this on an existing Linux machine. I don't want to log in to the box though. Is there any chance the TUI could "autostart"?*

Yes — and for your Rackwatch project, you can have the TUI start automatically at boot, without logging in at all. You don't need a desktop environment, a graphical session, or an interactive SSH connection.

On Linux, I'd run Rackwatch as a `systemd` service attached to a virtual terminal (TTY). When the machine boots, it launches the dashboard directly on the screen connected to that machine.

The important distinction is that a normal `systemd` service runs in the background, whereas your TUI needs to own a terminal to render on the physical display. We can configure both together.

## Recommended setup

Linux boots

systemd starts Rackwatch

No login required

Rackwatch takes over a virtual terminal

TTY1 · native-resolution display

### RACKWATCH · ONLINE

Dashboard visible immediately after boot

## 1. Create a systemd service

Assuming your Go binary is installed at `/opt/rackwatch/rackwatch`, create:

`/etc/systemd/system/rackwatch.service`

```sh

[Unit]
Description=Rackwatch terminal dashboard
After=local-fs.target
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=/opt/rackwatch/rackwatch
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

```

This is the basic configuration. For a production installation, I'd also run the application with a dedicated unprivileged service account, ensure it can access its configuration, and arrange for the service to own the terminal without competing with a login prompt.

## 2. Enable it at boot

Run these commands once, as an administrator:

```sh
sudo systemctl daemon-reload
sudo systemctl enable rackwatch.service
sudo systemctl start rackwatch.service
```

Important: TTY1 may already be occupied by Linux's login prompt (`getty`). If it is, disable that prompt so it doesn't compete with Rackwatch:

```sh
sudo systemctl disable --now getty@tty1.service
```

Keep SSH enabled so you can administer the machine remotely.

You can then check the dashboard service remotely:

```sh
systemctl status rackwatch.service
journalctl -u rackwatch.service -f
```

If the dashboard crashes, `Restart=always` tells systemd to launch it again.

## 3. A few things to consider

- Boot splash: You may briefly see boot messages before Rackwatch starts. You can configure a quieter boot later.
- Resolution: This service doesn't configure the display mode. Make sure Linux itself outputs 1424 × 280 before troubleshooting the TUI layout.
- Security: Don't run the monitoring application as root unless it genuinely needs elevated privileges.
- Recovery: Test a reboot and a simulated application failure before relying on it as an appliance.

One subtle point: `TTYPath=/dev/tty1` assumes the display is attached to the machine running Rackwatch. If the screen is connected to another device, this approach needs to change.

My recommendation: Use a system-level systemd service attached to TTY1. It is lightweight, reliable, and a natural fit for an always-on, read-only Go TUI. No manual login is needed after the initial setup.

---

To enable a login prompt on TTY2:

```sh
sudo systemctl enable --now getty@tty2.service
```

Keep your Rackwatch service attached to TTY1 and don't disable `getty@tty2.service`.
