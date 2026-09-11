package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
)

// Streaming knobs are adjustable at runtime from the UI: what's comfortable on
// wifi is wasteful on mobile data, so the user changes them mid-session rather
// than editing config.json and restarting.
//
// cfg is read by every stream goroutine on every frame, so all access goes
// through this lock.
var cfgMu sync.RWMutex

func liveCfg() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

// settingsPatch carries only what the UI can change; nil fields stay as they are.
type settingsPatch struct {
	Monitor *int `json:"monitor"`
	FPS     *int `json:"fps"`
	Quality *int `json:"quality"`
	MaxW    *int `json:"max_width"`
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var p settingsPatch
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		n := numDisplays()

		cfgMu.Lock()
		if p.Monitor != nil {
			cfg.Monitor = clampInt(*p.Monitor, 0, n-1)
		}
		if p.FPS != nil {
			cfg.FPS = clampInt(*p.FPS, 1, 30)
		}
		if p.Quality != nil {
			cfg.Quality = clampInt(*p.Quality, 20, 95)
		}
		if p.MaxW != nil {
			// 0 keeps the native width; anything else is a sane downscale range.
			if *p.MaxW != 0 {
				cfg.MaxW = clampInt(*p.MaxW, 480, 3840)
			} else {
				cfg.MaxW = 0
			}
		}
		saved := cfg
		cfgMu.Unlock()

		// Persist so the choice survives a restart.
		if data, err := json.MarshalIndent(saved, "", "  "); err == nil {
			if err := os.WriteFile(configPath(), data, 0600); err != nil {
				log.Printf("[pc-remote] could not write config: %v", err)
			}
		}
		log.Printf("[pc-remote] settings: monitor=%d fps=%d q=%d maxw=%d",
			saved.Monitor, saved.FPS, saved.Quality, saved.MaxW)
	}

	c := liveCfg()
	b := displayBounds(c.Monitor)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"monitor":   c.Monitor,
		"monitors":  numDisplays(),
		"fps":       c.FPS,
		"quality":   c.Quality,
		"max_width": c.MaxW,
		// Tap-ready links with the token already in them, regenerated from the
		// current addresses so DHCP moving the machine doesn't strand anyone.
		"lan_urls": lanURLs(listenPort(c), c.Token),
		"h264":     h264Info(),
		"width":    b.Dx(),
		"height":   b.Dy(),
	})
}

// h264Info tells the UI whether the picture can be produced here at all (it
// needs ffmpeg), which codec families this machine can encode, and the encoder
// behind each one — the browser picks the family, so naming a single encoder
// would name the wrong one.
func h264Info() map[string]any {
	ff, ok := h264Available()
	info := map[string]any{"available": ok}
	if !ok {
		return info
	}
	fams := []string{"h264"}
	for _, f := range []struct{ name, enc string }{{"hevc", "hevc_nvenc"}, {"av1", "av1_nvenc"}} {
		if encoderUsable(ff, f.enc) {
			fams = append(fams, f.name)
		}
	}
	info["families"] = fams

	encoders := map[string]string{}
	for _, fam := range fams {
		encoders[fam] = pickEncoderFor(ff, fam)
	}
	info["encoders"] = encoders

	// The bitrate the encoder is actually told to hit, so the UI can quote a
	// real number instead of guessing from pixel counts.
	c := liveCfg()
	b := displayBounds(c.Monitor)
	outW, outH := outputSize(c, b.Dx(), b.Dy())
	info["kbps"] = h264Bitrate(c, outW, outH)
	info["width"] = outW
	info["height"] = outH
	return info
}
