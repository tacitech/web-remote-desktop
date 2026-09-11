// pc-remote — a single-binary "look at my PC from a phone browser and click"
// remote. Captures the screen, encodes it as H.264 and injects mouse/keyboard
// from the browser. No agent to install on the phone; no external services.
//
// Deliberately self-contained and separate from the camera server: its own Go
// module, own tunnel, own folder. Meant to run behind a Cloudflare tunnel so a
// phone can reach https://<sub>.<domain> without opening any ports.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

//go:embed web/*
var webFS embed.FS

// Config is loaded from config.json next to the exe (created on first run).
type Config struct {
	Addr    string `json:"addr"`      // listen address (default 127.0.0.1:7070 — tunnel-only)
	Token   string `json:"token"`     // shared secret required on every request
	Monitor int    `json:"monitor"`   // display index (0 = primary)
	FPS     int    `json:"fps"`       // stream frame rate
	Quality int    `json:"quality"`   // JPEG quality 1..100
	MaxW    int    `json:"max_width"` // downscale wider frames (0 = native)
	// KeepAwake: when to hold the machine out of sleep so it stays reachable.
	// "ac" (default) = only on mains power, "always", "never". The display may
	// still turn off; a viewer connecting wakes it.
	KeepAwake string `json:"keep_awake"`
}

func defaultConfig() Config {
	return Config{
		Addr:      "127.0.0.1:7070",
		Token:     randToken(),
		Monitor:   0,
		FPS:       12,
		Quality:   65,
		MaxW:      1600,
		KeepAwake: "ac",
	}
}

var cfg Config

func main() {
	log.SetFlags(log.LstdFlags)
	setupLogFile()
	loadConfig()

	n := numDisplays()
	if cfg.Monitor >= n {
		log.Printf("[pc-remote] monitor %d does not exist (have %d) — using 0", cfg.Monitor, n)
		cfg.Monitor = 0
	}

	mux := http.NewServeMux()
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("[pc-remote] embed web: %v", err)
	}
	mux.Handle("/", auth(http.FileServer(http.FS(sub))))
	mux.Handle("/input", auth(http.HandlerFunc(inputHandler)))
	mux.Handle("/screen", auth(http.HandlerFunc(screenHandler)))
	mux.Handle("/settings", auth(http.HandlerFunc(settingsHandler)))
	mux.Handle("/ws", auth(http.HandlerFunc(wsInputHandler)))
	mux.Handle("/h264", auth(http.HandlerFunc(h264StreamHandler)))

	// Keep the tunnel alive for as long as the server runs.
	go superviseTunnel(context.Background())
	// A sleeping PC drops the tunnel and can't be woken from a phone — hold it
	// awake per policy (see wake_windows.go).
	go keepAwake(func() string { return liveCfg().KeepAwake })

	addr := listenAddr(cfg)
	log.Printf("[pc-remote] listening on %s | monitor=%d fps=%d q=%d maxw=%d", addr, cfg.Monitor, cfg.FPS, cfg.Quality, cfg.MaxW)
	log.Printf("[pc-remote] mo:  http://127.0.0.1:%d/?t=%s", listenPort(cfg), cfg.Token)
	for _, u := range lanURLs(listenPort(cfg), cfg.Token) {
		log.Printf("[pc-remote] LAN: %s", u)
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		if isAddrInUse(err) {
			// Another copy already holds the port — that one is doing the job.
			log.Printf("[pc-remote] another instance is already listening on %s — exiting", addr)
			return
		}
		log.Fatalf("[pc-remote] listen: %v", err)
	}
}

// isAddrInUse reports whether a listen failed purely because the port is taken.
func isAddrInUse(err error) bool {
	var se *os.SyscallError
	if errors.As(err, &se) {
		return errors.Is(se.Err, syscall.EADDRINUSE) || strings.Contains(se.Err.Error(), "in use") ||
			strings.Contains(se.Err.Error(), "normally permitted") // Windows WSAEADDRINUSE wording
	}
	return strings.Contains(err.Error(), "in use") || strings.Contains(err.Error(), "normally permitted")
}

// setupLogFile sends log output to a file beside the exe.
//
// The binary is built as a GUI app (-H windowsgui) so nothing pops a console
// window when it starts at logon — which also means stdout goes nowhere. Keep
// the log on disk, capped, so there's still something to read when it misbehaves.
func setupLogFile() {
	p := filepath.Join(exeDir(), "pc-remote.log")
	// Don't let it grow forever on a machine that's always on.
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.Printf("---- pc-remote starting ----")
}

func configPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}

func loadConfig() {
	p := configPath()
	b, err := os.ReadFile(p)
	if err != nil {
		cfg = defaultConfig()
		if data, e := json.MarshalIndent(cfg, "", "  "); e == nil {
			_ = os.WriteFile(p, data, 0600)
			log.Printf("[pc-remote] created default config: %s (token generated)", p)
		}
		return
	}
	cfg = defaultConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalf("[pc-remote] config.json is invalid: %v", err)
	}
	if cfg.Token == "" {
		cfg.Token = randToken()
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 12
	}
	if cfg.Quality <= 0 || cfg.Quality > 100 {
		cfg.Quality = 65
	}
}

// auth wraps a handler with a constant-time token check. The token may arrive
// as ?t=, header X-Token, or the cookie set after a successful login — so the
// everyday URL is just https://<host>/ with nothing appended.
func auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("t")
		if tok == "" {
			tok = r.Header.Get("X-Token")
		}
		if tok == "" {
			if c, err := r.Cookie("pcr"); err == nil {
				tok = c.Value
			}
		}
		want := liveCfg().Token
		if subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
			// Browsers get the login page; API callers get a plain 401.
			if wantsHTML(r) {
				serveLogin(w, http.StatusUnauthorized)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Persist the token so navigations and the video/websocket requests authenticate
		// without re-appending ?t= everywhere. One year: this is a personal tool
		// on a personal phone, and re-typing the code on every visit defeats it.
		http.SetCookie(w, &http.Cookie{
			Name: "pcr", Value: want, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600,
		})
		next.ServeHTTP(w, r)
	})
}

// wantsHTML is true for top-level browser navigations (so we can answer with a
// login page instead of a bare 401).
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/input", "/screen", "/settings", "/ws", "/h264":
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// serveLogin renders the code entry page: submitting it lands back on "/?t=..."
// which the auth wrapper accepts and turns into the long-lived cookie.
func serveLogin(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, loginHTML)
}

const loginHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>PC Remote</title>
<style>
 html,body{height:100%;margin:0;background:#0b1220;color:#e5e7eb;
   font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
   display:flex;align-items:center;justify-content:center}
 form{width:min(320px,86vw);text-align:center}
 h1{font-size:18px;font-weight:700;margin:0 0 4px}
 p{font-size:13px;color:#94a3b8;margin:0 0 18px}
 input{width:100%;height:48px;font-size:20px;text-align:center;letter-spacing:.18em;
   border-radius:10px;border:1px solid #1f2937;background:#111827;color:#e5e7eb;margin-bottom:12px}
 button{width:100%;height:46px;font-size:15px;font-weight:600;border:0;border-radius:10px;
   background:#3b82f6;color:#fff}
</style></head>
<body><form method="GET" action="/">
 <h1>PC Remote</h1><p>Enter the access code</p>
 <input name="t" inputmode="text" autocomplete="one-time-code" autocapitalize="off"
        autocorrect="off" spellcheck="false" autofocus placeholder="code">
 <button type="submit">Open</button>
</form></body></html>`

func screenHandler(w http.ResponseWriter, r *http.Request) {
	c := liveCfg()
	b := displayBounds(c.Monitor)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"width":    b.Dx(),
		"height":   b.Dy(),
		"monitor":  c.Monitor,
		"monitors": numDisplays(),
	})
}

// InputEvent is one action from the browser. Coordinates are normalized 0..1
// against the streamed image so they survive downscaling and DPI differences.
type InputEvent struct {
	Type   string  `json:"type"`   // move, down, up, click, dblclick, scroll, key, text
	X      float64 `json:"x"`      // 0..1
	Y      float64 `json:"y"`      // 0..1
	Button string  `json:"button"` // left, right, middle
	Delta  int     `json:"delta"`  // scroll amount (wheel notches)
	Key    string  `json:"key"`    // named key: Enter, Backspace, Tab, ArrowUp...
	Text   string  `json:"text"`   // literal text to type (unicode)
	Ctrl   bool    `json:"ctrl"`
	Alt    bool    `json:"alt"`
	Shift  bool    `json:"shift"`
	Meta   bool    `json:"meta"`
}

func inputHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var ev InputEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	injectInput(ev)
	w.WriteHeader(http.StatusNoContent)
}
