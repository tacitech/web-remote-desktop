package main

import (
	"context"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// The picture: captured frames piped through ffmpeg and out as fragmented MP4
// over a WebSocket.
//
// Inter-frame coding is the whole point. Watching a machine work means a screen
// that barely changes, and sending only the difference costs an order of
// magnitude less than sending every pixel every frame. The price is ffmpeg on
// the machine and a little buffering through MediaSource.

// h264Available reports whether the encode path can run at all.
func h264Available() (string, bool) {
	if p := ffmpegPath(); p != "" {
		return p, true
	}
	return "", false
}

func ffmpegPath() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	for _, p := range []string{
		exeDir() + `\ffmpeg.exe`,
		`C:\ffmpeg\bin\ffmpeg.exe`,
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return ""
}

// pickEncoder prefers the GPU: NVENC/QSV/AMF leave the CPU free for whatever
// the machine is actually doing, which matters when the reason you're watching
// remotely is that the PC is busy. libx264 is the portable fallback.
var cachedEncoder string

// pickEncoderFor resolves a requested family to a usable encoder, degrading to
// H.264 rather than failing when the GPU or ffmpeg build can't oblige.
func pickEncoderFor(ff, family string) string {
	switch family {
	case "av1":
		for _, e := range []string{"av1_nvenc", "av1_qsv", "av1_amf"} {
			if encoderUsable(ff, e) {
				return e
			}
		}
	case "hevc":
		for _, e := range []string{"hevc_nvenc", "hevc_qsv", "hevc_amf"} {
			if encoderUsable(ff, e) {
				return e
			}
		}
	}
	return pickEncoder(ff)
}

var encoderCache = map[string]bool{}

func encoderUsable(ff, enc string) bool {
	if v, ok := encoderCache[enc]; ok {
		return v
	}
	listCmd := exec.Command(ff, "-hide_banner", "-encoders")
	hideConsole(listCmd)
	out, _ := listCmd.Output()
	ok := strings.Contains(string(out), enc) && probeEncoder(ff, enc)
	encoderCache[enc] = ok
	return ok
}

func pickEncoder(ff string) string {
	if cachedEncoder != "" {
		return cachedEncoder
	}
	listCmd := exec.Command(ff, "-hide_banner", "-encoders")
	hideConsole(listCmd)
	out, _ := listCmd.Output()
	list := string(out)
	for _, enc := range []string{"h264_nvenc", "h264_qsv", "h264_amf"} {
		if strings.Contains(list, enc) && probeEncoder(ff, enc) {
			cachedEncoder = enc
			return enc
		}
	}
	cachedEncoder = "libx264"
	return cachedEncoder
}

// Listed != usable (no GPU present, driver too old), so encode one throwaway
// frame before committing to it.
func probeEncoder(ff, enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=256x144:d=0.1",
		"-c:v", enc, "-f", "null", "-")
	hideConsole(cmd)
	return cmd.Run() == nil
}

// encoderArgs favours constant QUALITY over constant bitrate.
//
// CBR is the wrong fit for a desktop: the screen is motionless most of the
// time, and a fixed bitrate makes the encoder pad the stream to hit its target
// anyway. Quality-targeted rate control spends bits only when pixels change —
// an idle screen costs almost nothing — while maxrate still caps the bursts
// when something does move.
func encoderArgs(enc string, fps, kbps, quality int) []string {
	max := strconv.Itoa(kbps) + "k"
	buf := strconv.Itoa(kbps/2) + "k"
	switch enc {
	case "av1_nvenc":
		cq := 52 - (quality-20)*16/75
		return []string{"-c:v", enc, "-preset", "p4", "-tune", "ll",
			"-rc", "vbr", "-cq", strconv.Itoa(cq), "-b:v", "0",
			"-maxrate", max, "-bufsize", buf, "-delay", "0"}
	case "hevc_nvenc":
		cq := 42 - (quality-20)*14/75
		return []string{"-c:v", enc, "-preset", "p4", "-tune", "ll",
			"-rc", "vbr", "-cq", strconv.Itoa(cq), "-b:v", "0",
			"-maxrate", max, "-bufsize", buf, "-delay", "0", "-zerolatency", "1"}
	case "av1_qsv", "av1_amf", "hevc_qsv", "hevc_amf":
		return []string{"-c:v", enc, "-b:v", strconv.Itoa(kbps) + "k", "-maxrate", max}
	case "h264_nvenc":
		// cq 20 (sharp) .. 34 (thrifty), from the same quality slider.
		cq := 34 - (quality-20)*14/75
		return []string{"-c:v", enc, "-preset", "p4", "-tune", "ll",
			"-rc", "vbr", "-cq", strconv.Itoa(cq),
			"-b:v", "0", "-maxrate", max, "-bufsize", buf,
			"-delay", "0", "-zerolatency", "1",
			// Skip encoding frames that are identical to the last one: a still
			// screen then emits almost no data at all.
			"-no-scenecut", "0"}
	case "h264_qsv":
		q := 34 - (quality-20)*14/75
		return []string{"-c:v", enc, "-preset", "veryfast", "-global_quality", strconv.Itoa(q),
			"-maxrate", max, "-low_power", "0"}
	case "h264_amf":
		return []string{"-c:v", enc, "-usage", "ultralowlatency", "-rc", "vbr_latency",
			"-qp_i", "26", "-qp_p", "28", "-maxrate", max}
	default:
		crf := 33 - (quality-20)*13/75
		return []string{"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
			"-crf", strconv.Itoa(crf), "-maxrate", max, "-bufsize", buf}
	}
}

// h264StreamHandler pumps captured frames through ffmpeg and forwards the
// fragmented-MP4 output over the socket, which is what MediaSource can play.
func h264StreamHandler(w http.ResponseWriter, r *http.Request) {
	ff, ok := h264Available()
	if !ok {
		http.Error(w, "ffmpeg not found", http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{"*"},
		CompressionMode: websocket.CompressionDisabled, // already compressed
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	// Someone is watching: make sure there is actually a picture to watch. A
	// powered-down display presents nothing, and a stream of nothing is the
	// famous pitch-black screen.
	viewerArrived()
	defer viewerLeft()

	cfgSnap := liveCfg()
	// The phone's own watchdog reconnects after 6s of silence; stay under it.
	wakeStart := time.Now()
	first, err := captureAfterWake(cfgSnap.Monitor, 5*time.Second)
	if err != nil {
		log.Printf("[pc-remote] screen capture failed after wake: %v", err)
		return
	}
	if d := time.Since(wakeStart); d > 300*time.Millisecond {
		log.Printf("[pc-remote] display woke after %.1fs", d.Seconds())
	}
	srcW, srcH := first.Bounds().Dx(), first.Bounds().Dy()
	outW, outH := outputSize(cfgSnap, srcW, srcH)

	// Codec family requested by the client (?codec=av1|hevc|h264). AV1 and HEVC
	// compress a static desktop far better, but browser support is uneven — the
	// client only asks for one after checking it can decode it.
	family := r.URL.Query().Get("codec")
	enc := pickEncoderFor(ff, family)
	kbps := h264Bitrate(cfgSnap, outW, outH)
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", srcW, srcH),
		"-framerate", strconv.Itoa(cfgSnap.FPS),
		"-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:%d", outW, outH),
		"-pix_fmt", "yuv420p",
		// Keyframes are the most expensive frames there are, and on a mostly
		// static screen one per second is nearly all of the bitrate. Every 5s is
		// still a tolerable wait for a picture when joining or recovering.
		"-g", strconv.Itoa(cfgSnap.FPS * 5),
	}
	args = append(args, encoderArgs(enc, cfgSnap.FPS, kbps, cfgSnap.Quality)...)
	args = append(args, "-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof+delay_moov",
		"-frag_duration", "100000", // 100ms fragments keep latency down
		"pipe:1")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, ff, args...)
	hideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[pc-remote] h264 start: %v", err)
		return
	}
	log.Printf("[pc-remote] h264 %dx%d @%dfps %dkbps (%s)", outW, outH, cfgSnap.FPS, kbps, enc)
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Feed frames.
	go func() {
		defer stdin.Close()
		ticker := time.NewTicker(time.Second / time.Duration(cfgSnap.FPS))
		defer ticker.Stop()
		last := first
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			img, err := captureDisplay(liveCfg().Monitor)
			if err != nil {
				img = last // keep the pipeline fed; a stalled encoder stalls playback
			} else {
				last = img
			}
			if img.Bounds().Dx() != srcW || img.Bounds().Dy() != srcH {
				return // resolution changed; the client reconnects and renegotiates
			}
			if _, err := stdin.Write(pixelBytes(img)); err != nil {
				return
			}
		}
	}()

	// Forward encoded output.
	buf := make([]byte, 32*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			werr := c.Write(wctx, websocket.MessageBinary, buf[:n])
			wcancel()
			if werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("[pc-remote] h264 read: %v", err)
			}
			return
		}
	}
}

// outputSize applies the width cap and rounds to even dimensions, which every
// encoder insists on.
func outputSize(c Config, srcW, srcH int) (int, int) {
	outW, outH := srcW, srcH
	if c.MaxW > 0 && outW > c.MaxW {
		outW = c.MaxW
		outH = srcH * outW / srcW
	}
	return outW &^ 1, outH &^ 1
}

// h264Bitrate maps the existing quality slider onto a bitrate: same control,
// same expectation, whichever codec is in use.
func h264Bitrate(c Config, w, h int) int {
	pixels := float64(w * h)
	// ~0.06 bits per pixel per frame at mid quality, scaled by the setting.
	bpp := 0.03 + float64(c.Quality)/100*0.09
	kbps := int(pixels * bpp * float64(c.FPS) / 1000)
	if kbps < 200 {
		kbps = 200
	}
	if kbps > 20000 {
		kbps = 20000
	}
	return kbps
}

func pixelBytes(img *image.RGBA) []byte { return img.Pix }
