//go:build !server

package server

import (
	"log"

	"github.com/gorilla/websocket"
)

// handleRelayConnection is a stub for non-server builds.
// Relay handling code is excluded to reduce binary ML profile.
func (s *Server) handleRelayConnection(conn *websocket.Conn, firstData []byte) {
	log.Printf("[server] relay mode not available in this build")
	conn.Close()
}