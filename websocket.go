// Package sshproxy provides WebSocket transport for SSH connections.
package sshproxy

import (
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/chainguard-dev/clog"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow connections from any origin for now
		// In production, you'd want to restrict this
		// TODO: Implement proper origin checking
		return true
	},
}

// WebSocketServer handles WebSocket connections that tunnel SSH
type WebSocketServer struct {
	sshConfig *ssh.ServerConfig
	handler   func(ssh.Channel, <-chan *ssh.Request, *ssh.Permissions)
}

// NewWebSocketServer creates a new SSH-over-WebSocket server
func NewWebSocketServer(sshConfig *ssh.ServerConfig, handler func(ssh.Channel, <-chan *ssh.Request, *ssh.Permissions)) *WebSocketServer {
	return &WebSocketServer{
		sshConfig: sshConfig,
		handler:   handler,
	}
}

// ServeHTTP handles WebSocket upgrade and SSH tunneling
func (s *WebSocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := clog.FromContext(ctx)

	log.Info("WebSocket connection request", "remote", r.RemoteAddr)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	log.Info("WebSocket connection established", "remote", r.RemoteAddr)

	// Create bidirectional pipes for SSH connection
	clientReader, clientWriter := io.Pipe()
	serverReader, serverWriter := io.Pipe()

	// Handle SSH connection in a goroutine
	go func() {
		sshConn, chans, reqs, err := ssh.NewServerConn(&pipeConn{
			Reader: clientReader,
			Writer: serverWriter,
		}, s.sshConfig)
		if err != nil {
			log.Error("SSH handshake failed", "error", err)
			_ = clientWriter.Close()
			_ = serverWriter.Close()
			return
		}
		defer sshConn.Close()

		log.Info("SSH connection established", "user", sshConn.User())

		// Handle SSH requests
		go ssh.DiscardRequests(reqs)

		// Handle SSH channels
		for newChannel := range chans {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
				continue
			}

			channel, requests, err := newChannel.Accept()
			if err != nil {
				log.Error("Failed to accept channel", "error", err)
				continue
			}

			go s.handler(channel, requests, sshConn.Permissions)
		}
	}()

	// Bridge WebSocket and SSH connections
	errCh := make(chan error, 2)

	// WebSocket -> SSH
	go func() {
		defer clientWriter.Close()
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				log.Debug("WebSocket read error", "error", err)
				errCh <- err
				return
			}

			log.Debug("Received WebSocket message", "type", messageType, "size", len(message))
			if messageType == websocket.BinaryMessage {
				_, err = clientWriter.Write(message)
				if err != nil {
					log.Debug("Pipe write error", "error", err)
					errCh <- err
					return
				}
			} else if messageType == websocket.TextMessage {
				// Handle base64-encoded binary data for browser compatibility
				decoded, err := base64.StdEncoding.DecodeString(string(message))
				if err != nil {
					log.Warn("Failed to decode base64 message", "error", err)
					continue
				}
				_, err = clientWriter.Write(decoded)
				if err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	// SSH -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := serverReader.Read(buf)
			if err != nil {
				errCh <- err
				return
			}

			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Wait for error or connection close
	<-errCh
	log.Info("WebSocket connection closed", "remote", r.RemoteAddr)
}

// pipeConn wraps io.Reader and io.Writer to implement net.Conn
type pipeConn struct {
	io.Reader
	io.Writer
}

func (c *pipeConn) Close() error {
	if closer, ok := c.Writer.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := c.Reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return nil
}

func (c *pipeConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *pipeConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }