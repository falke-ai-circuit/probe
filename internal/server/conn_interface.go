package server

import (
	"github.com/gorilla/websocket"
)

// Conn is the interface for agent connections. Both *websocket.Conn (direct)
// and *virtualConn (relayed) satisfy this interface.
type Conn interface {
	WriteJSON(v interface{}) error
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// Ensure *websocket.Conn satisfies Conn at compile time.
var _ Conn = (*websocket.Conn)(nil)