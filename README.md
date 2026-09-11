# web-remote-desktop

See and control your Windows PC from any phone browser. One small program on
the PC, nothing to install on the phone.

```
phone browser  ──HTTPS/WSS──►  Cloudflare Tunnel  ──►  pc-remote.exe on your PC
   (any)                        (free, no ports)         capture · encode · input
```

- **No app on the phone.** Open a URL, type a short code, you're in. Works in
  Chrome, Safari (iOS 17+), Firefox.
- **One exe on the PC.** No service, no installer, no relay server. Runs
  hidden, starts on login, supervises its own tunnel.
- **Light on bandwidth.** The screen is captured on the GPU (Desktop
  Duplication), encoded on the GPU (NVENC / Quick Sync / AMF) and played by
  the browser's native decoder. A mostly-still desktop costs about 0.5–1 Mbps;
  AV1 or HEVC is used when the phone can decode it.
- **Made for touch.** Tap where you want to click, or glide a cursor like a
  touchpad. Pinch to zoom, a loupe for small targets, hold-to-drag, modifier
  keys, and the phone's own keyboard for typing.
- **Reachable from anywhere** through a Cloudflare Tunnel — no port
  forwarding, no public IP. On the same Wi-Fi it offers a direct LAN link.
- **No server, no account, nothing to sign up for.** There is no relay or
  cloud service behind this project — the exe on your PC *is* the whole
  thing, and it never phones home. Your screen goes from your PC to your
  phone; on the LAN it doesn't leave your network at all. The only third
  party is Cloudflare, and only if you choose the tunnel (you can use a VPN
  or your own reverse proxy instead).

It started as a way to glance at a PC that was busy — a long build, an
assistant working through a task — and poke it now and then from the couch,
without setting up a full remote-desktop stack for a thirty-second look.

## Requirements

- Windows 10 or 11. (The code builds on Linux and macOS, but capture and input
  there are untested.)
- [ffmpeg](https://www.gyan.dev/ffmpeg/builds/) on your `PATH`, next to the
  exe, or at `C:\ffmpeg\bin\ffmpeg.exe`. The gyan.dev "full" builds include
  every hardware encoder.
- For remote access: [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
  and a domain on Cloudflare (a free plan is enough).
- To build from source: Go 1.21 or newer (a newer toolchain is fetched
  automatically if `go.mod` asks for one).

## Quick start (same Wi-Fi)

1. Build it, or grab `pc-remote.exe` from Releases:

   ```
   git clone https://github.com/tacitech/web-remote-desktop
   cd web-remote-desktop
   build.bat
   ```

2. Run `pc-remote.exe`. It runs hidden (no window). The first run creates
   `config.json` next to the exe; **your access code is the `token` field in
   that file**, generated randomly:

   ```json
   {
     "addr": "127.0.0.1:7070",
     "token": "k7x2m9pq4d",      <-- this is the code
     ...
   }
   ```

   `pc-remote.log` (also next to the exe) prints a ready-made link with the
   code filled in — the values below are examples, yours will differ:

   ```
   [pc-remote] listening on :7070 | monitor=0 fps=12 q=65 maxw=1600
   [pc-remote] LAN: http://192.168.1.23:7070/?t=k7x2m9pq4d
   ```

3. On your phone, open `http://<your-pc-ip>:7070/` and enter the code, or
   open the link from the log directly. Either way a cookie is set, so from
   then on the bare address is enough.

   To change the code (and log every device out), edit `token` in
   `config.json` and restart the exe.

## Remote access (Cloudflare Tunnel)

This is what makes it useful: your PC gets a hostname like
`pc.example.com` that works from anywhere, with TLS, without touching your
router.

1. Put your domain on Cloudflare (free plan).
2. Install cloudflared and log in once:

   ```
   cloudflared tunnel login
   ```

3. In the folder with `pc-remote.exe`, as Administrator:

   ```
   setup.bat example.com pc
   ```

   This creates a tunnel named after your PC, points `pc.example.com` at it,
   writes `cloudflared-config.yml`, registers a start-on-login task, and
   prints your access code.

4. `start.bat` (or just run `pc-remote.exe`). The tunnel is started and
   supervised by the exe itself — if cloudflared exits, it is restarted with
   backoff. `tunnel.log` has its output.

5. Open `https://pc.example.com/` on your phone and enter the code.

**Protect it.** The page can do anything you can do at the keyboard. The
access code is required on every request, but for anything exposed to the
internet, put Cloudflare Access in front of the hostname (Zero Trust →
Access → Applications → self-hosted, email one-time-PIN). It's free for small
teams and adds a real login before the page is even reachable.

## Start on login

`setup.bat` registers a scheduled task that launches `pc-remote.exe` at
logon. To do it by hand:

```
schtasks /Create /TN "PC Remote" /TR "C:\path\to\pc-remote.exe" /SC ONLOGON /RL HIGHEST /F
```

The exe refuses to start if another instance already holds the port, so a
cheap watchdog is to also run it every few minutes — if it died, this brings
it back; if it's alive, the new one exits immediately:

```
schtasks /Create /TN "PC Remote" /TR "C:\path\to\pc-remote.exe" /SC MINUTE /MO 5 /RL HIGHEST /F
```

## Using it on the phone

Two pointing models, switchable with the first toolbar button:

| Mode | How it works |
|---|---|
| **Direct** (default) | Tap where you want to click. Pinch to zoom in first when the target is small. |
| **Touchpad** | Drag anywhere to glide a cursor, tap to click where the cursor is. Better when your finger would cover the target. |

Gestures and buttons:

- Tap = left click · **Right** then tap = right click · **Double** = double
  click · **Hold** = press and hold the left button, move, tap again to release
- Two-finger drag = scroll · pinch = zoom · **Fit** resets the view
- **Loupe** shows a magnified view of what's under your finger, placed in the
  empty part of the screen (above the picture in portrait, beside it in
  landscape)
- **⌨** opens the keys tray: the phone keyboard for typing, Ctrl/Alt/Shift as
  sticky modifiers, Enter/Esc/Tab/arrows
- **⚙** opens settings: monitor, resolution, frame rate, quality, and the
  LAN shortcut. Changes apply to the running stream without reconnecting.

Two dots in the corner show the picture link and the control link. Green is
good; the control dot turns amber if the network blocks WebSockets and input
falls back to plain HTTP (slower, but still works).

## Configuration

`config.json` is created on first run next to the exe:

| Field | Default | Meaning |
|---|---|---|
| `addr` | `127.0.0.1:7070` | Port to listen on (the host part is ignored; it binds every interface so both the tunnel and the LAN can reach it) |
| `token` | random | Access code. Change it to log everyone out. |
| `monitor` | `0` | Which display to show (0 = primary) |
| `fps` | `12` | Frame rate |
| `quality` | `65` | 1–100, maps to the encoder's quality target |
| `max_width` | `1600` | Downscale wider frames (0 = native) |
| `keep_awake` | `"ac"` | Hold the PC out of sleep: `ac` (only on mains), `always`, `never`. A sleeping PC drops the tunnel and can't be woken from a phone. The display may still turn off — a viewer connecting wakes it. |

`fps`, `quality`, `max_width` and `monitor` can also be changed live from the
settings sheet; they are saved back to the file.

## How it works

```
 pc-remote.exe
 ┌────────────────────────────────────────────────────────────────────────┐
 │ capture: DXGI Desktop Duplication (GPU) ─ fallback GDI BitBlt          │
 │    │  frames only when the desktop actually changed                    │
 │    ▼                                                                   │
 │ encode: ffmpeg  av1/hevc/h264 _nvenc | _qsv | _amf  ─ fallback libx264 │
 │    │  fragmented MP4, 100 ms fragments, keyframe every 5 s             │
 │    ▼                                                                   │
 │ /h264  WebSocket ───────────────────────────► <video> via MediaSource  │
 │ /ws    WebSocket ◄──────────────────────────  touch/keys (coalesced)   │
 │ /input POST      ◄──────────────────────────  fallback when WS blocked │
 │ input: SendInput, per-monitor DPI aware                                │
 │ tunnel: cloudflared child process, http2, restarted with backoff       │
 └────────────────────────────────────────────────────────────────────────┘
```

A few things that turned out to matter:

- **Capture on the GPU.** `BitBlt` copies the whole desktop through the CPU
  every frame — 40–60 ms at 2560×1600, which alone caps the stream near
  12 fps. Desktop Duplication hands over the compositor's frame in GPU memory
  and says outright when nothing changed. On hybrid laptops the device has to
  be created on the adapter that actually owns the output; the discrete GPU
  reports a mirror that never delivers a frame.
- **Codec negotiation.** The page asks the browser which of AV1, HEVC, H.264
  it can decode via MediaSource, the server says which it can encode, and the
  best common one wins. Measured on a near-static desktop: H.264 ≈ 0.73,
  HEVC ≈ 0.57, AV1 ≈ 0.52 Mbps.
- **Input over one socket.** A POST per mouse move costs a full round trip
  through the tunnel; moves are coalesced per animation frame and sent over a
  WebSocket, with the POST path kept as a fallback for networks that block
  WebSockets.
- **DPI.** At 150 % scaling `SetCursorPos` silently clamps to two thirds of
  the screen unless the process is per-monitor DPI aware. It is.
- **Power.** A powered-off panel makes capture fail outright, so a viewer
  connecting wakes the display and holds it awake; the system itself is held
  out of sleep while on mains power (configurable).
- **Tunnel transport.** QUIC over UDP gave intermittent 520s on some
  connections; the tunnel is forced to HTTP/2.

## Troubleshooting

| Symptom | Look at |
|---|---|
| Page loads, no picture, settings say *ffmpeg not found* | Put ffmpeg on `PATH` or next to the exe |
| *This browser cannot play H.264 video* | iOS 16 and older have no MediaSource. Update iOS or use Chrome on Android |
| Picture is black or frozen | The display was off; it wakes on connect. If it keeps happening, `pc-remote.log` shows `display woke after …` |
| Clicks land in the wrong place | Check the **monitor** setting when you have more than one display |
| Works on LAN, `https://…` gives 520/530 | `tunnel.log`. Make sure cloudflared is logged in and the DNS route exists (`cloudflared tunnel list`, `cloudflared tunnel route dns …`) |
| Nothing at all | `pc-remote.log` next to the exe |

## Build and test

```
go build -ldflags="-H windowsgui" -o pc-remote.exe .   # or build.bat
go test .                                             # unit tests
set PCREMOTE_UI_TEST=1 && go test -run 'Capture|DarkPanel' -v .   # drives the real display
```

## License

MIT — see [LICENSE](LICENSE).
