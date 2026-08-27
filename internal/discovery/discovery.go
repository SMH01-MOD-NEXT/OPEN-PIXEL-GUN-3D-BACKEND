// SPDX-License-Identifier: GPL-3.0-or-later

// Package discovery answers LAN probes so the game can offer a "search on the
// local network" option next to manual address entry.
//
// The protocol is intentionally trivial: the client broadcasts the probe magic
// on UDP, every backend on the segment answers with a small JSON descriptor,
// and the client learns the server address from the source of that datagram.
// No address has to be guessed or hardcoded in the APK.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/SMH01/open-pixel-gun-3d-backend/internal/logs"
)

// Probe is the magic the client sends. It is a prefix match, so the client may
// append its own build tag after it.
const Probe = "OPG3D-DISCOVER-V1"

// Descriptor is the JSON answer sent back to a probing client.
type Descriptor struct {
	Magic    string `json:"magic"`
	Name     string `json:"name"`
	HTTPPort int    `json:"http_port"`
	APIPath  string `json:"api_path"`
	Version  string `json:"version"`
}

// Serve answers probes until ctx is cancelled.
func Serve(ctx context.Context, port int, descriptor Descriptor, log *logs.Logger) error {
	if port == 0 {
		log.Printf("discovery: disabled (port 0)")
		return nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return fmt.Errorf("discovery: listen udp :%d: %w", port, err)
	}
	answer, err := json.Marshal(descriptor)
	if err != nil {
		conn.Close()
		return fmt.Errorf("discovery: encode descriptor: %w", err)
	}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	log.Event("discovery.listen", map[string]any{"port": port, "name": descriptor.Name})

	buf := make([]byte, 512)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Event("discovery.error", map[string]any{"err": err.Error()})
			// A transient read error must not kill the responder.
			time.Sleep(100 * time.Millisecond)
			continue
		}
		payload := strings.TrimSpace(string(buf[:n]))
		if !strings.HasPrefix(payload, Probe) {
			log.Event("discovery.ignored", map[string]any{"peer": peer.String(), "payload": payload})
			continue
		}
		if _, err := conn.WriteToUDP(answer, peer); err != nil {
			log.Event("discovery.reply_failed", map[string]any{"peer": peer.String(), "err": err.Error()})
			continue
		}
		log.Event("discovery.reply", map[string]any{"peer": peer.String(), "payload": payload})
	}
}
