package sshproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/chainguard-dev/clog"
	"github.com/gorilla/websocket"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
)

// Proxy handles SSH-to-WebSocket proxying
type Proxy struct {
	WebsocketURL string
	SSHAddr      string
	SSHKeySecret string

	sshPrivateKey []byte
}

// NewProxy creates a new SSH proxy
func NewProxy(websocketURL, sshAddr, sshKeySecret string) *Proxy {
	return &Proxy{WebsocketURL: websocketURL, SSHAddr: sshAddr, SSHKeySecret: sshKeySecret}
}

// Start starts the SSH proxy server
func (p *Proxy) Start(ctx context.Context) error {
	log := clog.FromContext(ctx)

	// Retrieve SSH private key from Secret Manager.
	key, err := p.getSSHPrivateKey(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve SSH private key: %w", err)
	}
	p.sshPrivateKey = key
	log.Info("SSH private key loaded from Secret Manager", "secret", p.SSHKeySecret)

	listener, err := net.Listen("tcp", p.SSHAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.SSHAddr, err)
	}
	defer listener.Close()

	wsURL, err := url.Parse(p.WebsocketURL)
	if err != nil {
		return fmt.Errorf("failed to parse WebSocket URL: %w", err)
	}

	audience := fmt.Sprintf("https://%s", wsURL.Host)
	log.Info("Getting identity token for WebSocket authentication", "audience", audience)
	ts, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return fmt.Errorf("failed to create token source: %w", err)
	}

	log.Info("SSH proxy listening", "address", p.SSHAddr, "forwarding", p.WebsocketURL)

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

		go ProxySSHToWebSocket(ctx, conn, wsURL, ts)
	}
}

// ProxySSHToWebSocket creates a bidirectional proxy between SSH and WebSocket
func ProxySSHToWebSocket(ctx context.Context, sshConn net.Conn, wsURL *url.URL, ts oauth2.TokenSource) error {
	token, err := ts.Token()
	if err != nil {
		return fmt.Errorf("failed to get identity token: %w", err)
	}

	clog.InfoContext(ctx, "Proxying SSH connection to WebSocket",
		"url", wsURL.String(),
		"local_addr", sshConn.LocalAddr().String(),
		"remote_addr", sshConn.RemoteAddr().String(),
	)

	// Connect to WebSocket with authentication
	header := http.Header{}
	header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	wsConn, resp, err := websocket.DefaultDialer.Dial(wsURL.String(), header)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
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

			if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
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

			if _, err := sshConn.Write(message); err != nil {
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

// getSSHPrivateKey retrieves the SSH private key from Secret Manager
func (p *Proxy) getSSHPrivateKey(ctx context.Context) ([]byte, error) {
	log := clog.FromContext(ctx)

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create Secret Manager client: %w", err)
	}
	defer client.Close()

	// Access the secret version
	result, err := client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: p.SSHKeySecret})
	if err != nil {
		return nil, fmt.Errorf("failed to access secret %s: %w", p.SSHKeySecret, err)
	}

	log.Info("Successfully retrieved SSH private key from Secret Manager", "secret", p.SSHKeySecret)
	return result.Payload.Data, nil
}

// GetSSHPrivateKey returns the loaded SSH private key
func (p *Proxy) GetSSHPrivateKey() []byte {
	return p.sshPrivateKey
}
