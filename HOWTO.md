# HOWTO

## Take a screenshot of the dashboard

rackwatch runs on the Linux text console, which has no X or Wayland, so ordinary screenshot tools don't work. Instead, read the framebuffer (`/dev/fb0`) with ffmpeg on the box that drives the rack screen.

### Before you start

- Install ffmpeg on the box: `sudo apt install ffmpeg`.
- Reading `/dev/fb0` needs root or membership in the `video` group. Check with `id`.
- Only what is on screen gets captured, so rackwatch's console must be the active one. `cat /sys/class/tty/tty0/active` shows which console that is.

### Capture

Log in to the box with `ssh` and run:

```sh
ffmpeg -loglevel error -y -f fbdev -i /dev/fb0 -frames:v 1 -update 1 \
  -pix_fmt rgb24 -vf crop=1352:264:40:16 rackwatch.png
```

Then copy the image to your machine:

```sh
scp <box>:rackwatch.png .
```

What the options do:

| Option | Purpose |
| --- | --- |
| `-f fbdev -i /dev/fb0` | Read from the framebuffer |
| `-frames:v 1 -update 1` | Write a single image. Without `-update 1`, ffmpeg warns that the filename has no sequence pattern such as `%03d` |
| `-pix_fmt rgb24` | Drop the alpha channel. The framebuffer often leaves alpha at 0, which makes the PNG fully transparent |
| `-vf crop=…` | Cut away the edges the panel hides (see below). Leave it out to keep the full 1424 × 280 screen |
| `-y` | Overwrite an existing `rackwatch.png` without asking |

### Cropping to the visible area

The crop takes the form `crop=width:height:x:y`. With an 8 × 16 font, work out the values from the `margin_*` settings in `config.toml`:

| Value | Formula | Default config |
| --- | --- | --- |
| width | 1424 − 8 × (`margin_left` + `margin_right`) | 1424 − 8 × 9 = 1352 |
| height | 280 − 16 × (`margin_top` + `margin_bottom`) | 280 − 16 × 1 = 264 |
| x | 8 × `margin_left` | 40 |
| y | 16 × `margin_top` | 16 |

### Troubleshooting

- **Empty (0 byte) PNG when running ffmpeg through `ssh box '…'`:** `sudo` can't ask for a password without a terminal, so ffmpeg never runs. Log in and run the command interactively, or add your user to the `video` group so no `sudo` is needed.
- **The PNG looks blank or transparent:** add `-pix_fmt rgb24` to the command.
- **The image is the wrong size or mostly black:** ffmpeg prints the framebuffer size (`w:… h:…`) when it starts. If it doesn't say 1424 × 280, you're on a box that isn't driving the rack screen, or the screen isn't connected.
