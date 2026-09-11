package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Input over a persistent socket instead of one HTTP request per event.
//
// A POST per mouse move costs a full round trip (150-300ms through the tunnel),
// and dragging a finger produces ~60 events a second — they queue up and the
// cursor crawls seconds behind the finger. Every real remote-desktop tool
// (RustDesk, VNC, RDP) keeps one connection open and streams events down it;
// this does the same. The client also coalesces moves, so what arrives here is
// already the latest position rather than a backlog of stale ones.
func wsInputHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Same-origin only; the page and the socket share the tunnel hostname.
		OriginPatterns:  []string{"*"},
		CompressionMode: websocket.CompressionDisabled, // events are tiny
	})
	if err != nil {
		log.Printf("[pc-remote] ws accept: %v", err)
		return
	}
	defer c.CloseNow()

	ctx := r.Context()
	for {
		// A live phone sends events or pings well within this; the read timeout
		// reaps sockets left behind when the phone sleeps or loses signal.
		readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		typ, data, err := c.Read(readCtx)
		cancel()
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var ev InputEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		if ev.Type == "ping" {
			continue // keeps the socket warm through idle NAT timeouts
		}
		injectInput(ev)
	}
}
