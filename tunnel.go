package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The tunnel is supervised in-process rather than by a batch loop.
//
// cloudflared gives up and exits ("no more connections active and exiting")
// when it loses every edge connection — a sleeping laptop or a brief ISP blip
// is enough. Left alone, the phone then gets Cloudflare error 1033 until
// someone comes back to the PC and starts it by hand, which defeats the point
// of a remote. Batch supervision turned out to be fragile (start/timeout
// misbehave in a hidden console), so the server owns the child directly.
func superviseTunnel(ctx context.Context) {
	dir := exeDir()
	cfgFile := filepath.Join(dir, "cloudflared-config.yml")
	if _, err := os.Stat(cfgFile); err != nil {
		log.Printf("[pc-remote] no cloudflared-config.yml — running on the local network only")
		return
	}
	bin := cloudflaredPath()
	if bin == "" {
		log.Printf("[pc-remote] cloudflared not found — running on the local network only")
		return
	}

	logFile := filepath.Join(dir, "tunnel.log")
	for attempt := 0; ctx.Err() == nil; attempt++ {
		lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			lf = nil
		}
		// QUIC (the default) rides on UDP, which many consumer ISPs throttle or
		// drop unevenly — the symptom is a tunnel that answers, then times out,
		// then returns 520, at random. HTTP/2 over TCP is a touch slower to
		// establish but survives those networks, which matters more here.
		args := []string{"tunnel", "--protocol", "http2", "--retries", "10",
			"--grace-period", "10s", "--config", cfgFile, "run"}
		cmd := exec.CommandContext(ctx, bin, args...)
		hideConsole(cmd)
		cmd.Dir = dir
		if lf != nil {
			cmd.Stdout, cmd.Stderr = lf, lf
		}
		start := time.Now()
		if err := cmd.Start(); err != nil {
			log.Printf("[pc-remote] could not start cloudflared: %v", err)
		} else {
			log.Printf("[pc-remote] tunnel running (pid %d)", cmd.Process.Pid)
			_ = cmd.Wait()
		}
		if lf != nil {
			_ = lf.Close()
		}
		if ctx.Err() != nil {
			return
		}
		// A tunnel that ran fine for a while is a transient failure — retry
		// immediately. Rapid exits mean something is wrong (bad config, no
		// network), so back off instead of spinning.
		delay := 2 * time.Second
		if time.Since(start) < 20*time.Second {
			delay = time.Duration(min(30, 2<<min(attempt, 4))) * time.Second
		} else {
			attempt = 0
		}
		log.Printf("[pc-remote] tunnel exited after %v — restarting in %v", time.Since(start).Truncate(time.Second), delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// cloudflaredPath prefers a copy sitting next to the exe (portable folder),
// then the standard install, then PATH.
func cloudflaredPath() string {
	candidates := []string{
		filepath.Join(exeDir(), "cloudflared.exe"),
		`C:\Program Files (x86)\cloudflared\cloudflared.exe`,
		`C:\Program Files\cloudflared\cloudflared.exe`,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return ""
}
