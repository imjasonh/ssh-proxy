package sshproxy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/chainguard-dev/clog"
	"github.com/gorilla/websocket"
	"google.golang.org/api/idtoken"
)

// ProxyConfig holds configuration for the SSH proxy
type ProxyConfig struct {
	WebsocketURL string
	SSHAddr      string
}

// Proxy handles SSH-to-WebSocket proxying
type Proxy struct {
	config ProxyConfig
}

// NewProxy creates a new SSH proxy
func NewProxy(config ProxyConfig) *Proxy {
	return &Proxy{config: config}
}

// Start starts the SSH proxy server
func (p *Proxy) Start(ctx context.Context) error {
	log := clog.FromContext(ctx)

	// Parse the URL to validate it
	u, err := url.Parse(p.config.WebsocketURL)
	if err != nil {
		return fmt.Errorf("invalid WebSocket URL: %w", err)
	}

	listener, err := net.Listen("tcp", p.config.SSHAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.config.SSHAddr, err)
	}
	defer listener.Close()

	log.Info("SSH proxy listening", "address", p.config.SSHAddr, "forwarding", p.config.WebsocketURL)

	// Accept SSH connections and proxy them to WebSocket
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := listener.Accept()
		if err != nil {
			log.Info("Failed to accept connection", "error", err)
			continue
		}

		go p.handleSSHConnection(ctx, conn, u)
	}
}

func (p *Proxy) handleSSHConnection(ctx context.Context, sshConn net.Conn, wsURL *url.URL) {
	ctx = clog.WithValues(ctx, "remote_addr", sshConn.RemoteAddr())
	log := clog.FromContext(ctx)
	defer sshConn.Close()

	// Get identity token for authentication to Cloud Run
	audience := fmt.Sprintf("https://%s", wsURL.Host)

	// Get ID token
	log.Info("Getting identity token for WebSocket authentication", "audience", audience)
	tokenSource, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		log.Info("Failed to create token source", "error", err)
		return
	}

	token, err := tokenSource.Token()
	if err != nil {
		log.Info("Failed to get identity token", "error", err)
		return
	}
	log.Info("Got identity token, expires at", "expiry", token.Expiry)

	// Create WebSocket dialer
	dialer := websocket.DefaultDialer

	// Create request headers with authentication
	header := http.Header{}
	header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	log.Info("Connecting to WebSocket URL with auth header", "wsURL", wsURL.String())

	// Connect to WebSocket with authentication
	wsConn, resp, err := dialer.Dial(wsURL.String(), header)
	if err != nil {
		log.Info("Failed to connect to WebSocket", "error", err)
		if resp != nil {
			log.Info("Response status", "status", fmt.Sprintf("%d %s", resp.StatusCode, resp.Status))
			_ = resp.Body.Close()
		}
		return
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer wsConn.Close()

	log.Info("Proxying SSH connection")

	// Create error channel for goroutines
	errCh := make(chan error, 2)

	// SSH -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := sshConn.Read(buf)
			if err != nil {
				errCh <- err
				return
			}

			// Send as binary message
			err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// WebSocket -> SSH
	go func() {
		for {
			messageType, message, err := wsConn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}

			var data []byte
			switch messageType {
			case websocket.BinaryMessage:
				data = message
			case websocket.TextMessage:
				// Decode base64 if text message
				decoded, err := base64.StdEncoding.DecodeString(string(message))
				if err != nil {
					log.Info("Failed to decode base64", "error", err)
					continue
				}
				data = decoded
			default:
				continue
			}

			_, err = sshConn.Write(data)
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Wait for error
	<-errCh
	log.Info("Connection closed for SSH")
}

// ProxySSHToWebSocket creates a bidirectional proxy between SSH and WebSocket
func ProxySSHToWebSocket(ctx context.Context, sshConn net.Conn, wsURL string) error {
	// Connect to WebSocket
	dialer := websocket.DefaultDialer
	wsConn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("connecting to WebSocket: %w", err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer wsConn.Close()

	// Create pipes for bidirectional communication
	errCh := make(chan error, 2)

	// SSH -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := sshConn.Read(buf)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					errCh <- err
				}
				return
			}

			err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// WebSocket -> SSH
	go func() {
		for {
			_, message, err := wsConn.ReadMessage()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					errCh <- err
				}
				return
			}

			_, err = sshConn.Write(message)
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}