// Package sshproxy provides WebSocket transport for SSH connections.
package sshproxy

import (
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/chainguard-dev/clog"
	"github.com/gorilla/websocket"
)

func ProxyWebSocketToSSH(tcpAddr string, upgrader websocket.Upgrader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log := clog.FromContext(r.Context())

		// Upgrade HTTP connection to WebSocket
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Error("Failed to upgrade WebSocket connection", "error", err)
			return
		}
		defer conn.Close()

		// Connect to local SSH server
		tcpConn, err := net.Dial("tcp", tcpAddr)
		if err != nil {
			log.Error("Failed to connect to SSH server", "error", err, "address", tcpAddr)
			return
		}
		defer tcpConn.Close()

		log.Info("WebSocket connection established", "remote_addr", r.RemoteAddr)

		// Create bidirectional proxy

		// WebSocket -> TCP
		done := make(chan string)
		go func() {
			defer func() {
				select {
				case done <- "ws_to_tcp_closed":
				default: // other goroutine already closed, don't block
				}
			}()
			for {
				messageType, data, err := conn.ReadMessage()
				if err != nil {
					if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
						log.Error("Unexpected WebSocket closure", "error", err)
					}
					// TODO: what other errors need to be handled here?
					return
				}
				if messageType == websocket.BinaryMessage {
					if _, err := tcpConn.Write(data); err != nil {
						log.Error("TCP write error", "error", err)
						return
					}
				}
			}
		}()

		// TCP -> WebSocket
		go func() {
			defer func() {
				select {
				case done <- "tcp_to_ws_closed":
				default: // other goroutine already closed, don't block
				}
			}()
			buf := make([]byte, 4096)
			for {
				n, err := tcpConn.Read(buf)
				if err != nil && !errors.Is(err, io.EOF) {
					log.Error("TCP read error", "error", err)
					return
				}
				if err = conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					log.Error("WebSocket write error", "error", err)
					return
				}
			}
		}()

		// Wait for either direction to close
		msg := <-done
		log.Info("WebSocket proxy connection closed", "reason", msg)
	}
}
