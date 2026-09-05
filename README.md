# screenshot-go

Personal-use desktop screenshot app built with Wails v3 for Linux and macOS. It lives in the system tray, captures the primary display, supports fast region/fullscreen workflows, lightweight annotation, clipboard copy, and PNG save.

As of September 4, 2026 this scaffold is pinned to:

- Go `1.27.1` via `toolchain go1.27.1`
- Wails v3 `v3.0.0-beta.16` released August 29, 2026

## Features

- Tray-only startup with no persistent main window
- Capture Region and Capture Fullscreen from the tray menu
- Region selection with dimmed outside mask
- Annotation tools: rectangle, circle/ellipse, arrow, text
- Color picker with per-annotation color persistence
- Copy composited PNG to the system clipboard
- Save composited PNG through a native save dialog
- Escape closes the overlay at any point and returns to tray-only mode
- Launch-at-login toggle in the tray menu using Wails autostart support

## Install / build

Prerequisites:

- Go `1.27.1` or newer in the `1.27` series
- Node.js and npm
- `wails3` CLI on your `PATH`

Frontend dependencies:

```bash
cd frontend
npm install
```

Development:

```bash
make check
make dev
```

Or directly with Wails:

```bash
wails3 dev
```

Production build:

```bash
make build
```

Or directly with Wails:

```bash
wails3 build
```

## Linux Wayland

Linux Wayland capture now uses compositor-specific backends:

- wlroots compositors such as Sway and Hyprland use `grim` via `grim -o <primary-output> -`
- GNOME Wayland uses the `org.gnome.Shell.Screenshot` D-Bus API through `busctl`

Install `grim` with one of:

```bash
sudo apt install grim
sudo pacman -S grim
sudo dnf install grim
sudo zypper install grim
```

On GNOME, `busctl` must also be available on `PATH` so the app can call GNOME Shell's screenshot service.

If your compositor does not expose the wlroots screencopy protocol and does not provide a compatible fallback, the app returns a compositor-specific error instead of silently failing.

## Autostart

### macOS

- The app is configured as an accessory app, so it runs without a Dock icon.
- `build/darwin/Info.plist` and `build/darwin/Info.dev.plist` set `LSUIElement=true`.
- For daily use, enable `Launch at Login` from the tray menu.
- You can also add the built `.app` bundle manually in `System Settings -> General -> Login Items`.

### Linux

- Enable `Launch at Login` from the tray menu to let Wails manage autostart for you.
- A manual `.desktop` file is included at `build/linux/screenshot-go.desktop`.
- To symlink it into autostart:

```bash
mkdir -p ~/.config/autostart
ln -sf "$(pwd)/build/linux/screenshot-go.desktop" ~/.config/autostart/screenshot-go.desktop
```

If your built binary is not on `PATH`, edit the `Exec=` line to point to the absolute binary path.

## Usage

1. Launch the app. It should show only a tray/menu bar icon with the tooltip `Screenshot`.
2. Pick `Capture Region` or `Capture Fullscreen`.
3. For region capture, drag to create the crop rectangle.
4. Annotate with rectangle, circle, arrow, or text.
5. Use `Copy` to send the composited PNG to the clipboard or `Save` to write it to disk.
6. Press `Escape` at any point to cancel and return to the tray.

## Notes

- This is intentionally a personal-use utility, not enterprise-focused software.
- v1 targets the primary display only.
- Windows, mobile, global hotkeys, undo/redo, blur, and multi-monitor selection are intentionally out of scope.

## Project layout

```text
.
├── main.go
├── capture_service.go
├── build
│   ├── darwin
│   └── linux
├── frontend
│   ├── bindings
│   ├── public
│   ├── src
│   └── dist
└── go.mod
```
