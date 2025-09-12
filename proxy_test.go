package sshproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	"golang.org/x/oauth2"
)

var ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "mock-token"})

func TestProxySSHToWebSocket_BidirectionalTraffic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wsConn *websocket.Conn
	var wsMutex sync.Mutex
	receivedFromSSH := make([][]byte, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check auth header
		if auth := r.Header.Get("Authorization"); auth != "Bearer mock-token" {
			t.Errorf("Expected auth header 'Bearer mock-token', got %q", auth)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade connection: %v", err)
			return
		}

		wsMutex.Lock()
		wsConn = conn
		wsMutex.Unlock()

		// Read messages from SSH connection via WebSocket
		for {
			if _, message, err := conn.ReadMessage(); err != nil {
				return
			} else {
				receivedFromSSH = append(receivedFromSSH, message)
			}
		}
	}))
	defer server.Close()

	// Convert to WebSocket URL
	wsURL, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))

	// Create SSH connection pair
	sshClient, sshServer := net.Pipe()
	defer sshClient.Close()
	defer sshServer.Close()

	// Track data received on SSH side
	var sshMutex sync.Mutex
	receivedFromWS := make([][]byte, 0)

	// Read from SSH server side in background
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := sshServer.Read(buf)
			if err != nil {
				return
			}
			sshMutex.Lock()
			receivedFromWS = append(receivedFromWS, append([]byte(nil), buf[:n]...))
			sshMutex.Unlock()
		}
	}()

	// Start the proxy
	go func() {
		if err := ProxySSHToWebSocket(ctx, sshClient, wsURL, ts); err != nil && ctx.Err() == nil {
			t.Errorf("ProxySSHToWebSocket failed: %v", err)
		}
	}()

	// Wait for WebSocket connection to be established
	time.Sleep(100 * time.Millisecond)

	// Test SSH -> WebSocket direction
	testDataSSHToWS := []byte("Hello from SSH to WebSocket!")
	if _, err := sshServer.Write(testDataSSHToWS); err != nil {
		t.Fatalf("Failed to write to SSH server: %v", err)
	}

	// Wait for message to propagate
	time.Sleep(100 * time.Millisecond)

	if len(receivedFromSSH) == 0 {
		t.Error("No data received from SSH connection via WebSocket")
	} else if string(receivedFromSSH[0]) != string(testDataSSHToWS) {
		t.Errorf("SSH->WebSocket: expected %q, got %q", testDataSSHToWS, receivedFromSSH[0])
	}

	// Test WebSocket -> SSH direction
	wsMutex.Lock()
	if wsConn != nil {
		testDataWSToSSH := []byte("Hello from WebSocket to SSH!")
		if err := wsConn.WriteMessage(websocket.BinaryMessage, testDataWSToSSH); err != nil {
			wsMutex.Unlock()
			t.Fatalf("Failed to write to WebSocket: %v", err)
		}
		wsMutex.Unlock()

		// Wait for message to propagate
		time.Sleep(100 * time.Millisecond)

		sshMutex.Lock()
		if len(receivedFromWS) == 0 {
			t.Error("No data received from WebSocket via SSH connection")
		} else if string(receivedFromWS[0]) != string(testDataWSToSSH) {
			t.Errorf("WebSocket->SSH: expected %q, got %q", testDataWSToSSH, receivedFromWS[0])
		}
		sshMutex.Unlock()
	} else {
		wsMutex.Unlock()
		t.Error("WebSocket connection was not established")
	}
}

func TestProxySSHToWebSocket_MultipleMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		// Echo messages back
		for {
			if _, message, err := conn.ReadMessage(); err != nil {
				return
			} else {
				// Echo back with prefix
				response := append([]byte("echo: "), message...)
				if err := conn.WriteMessage(websocket.BinaryMessage, response); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()

	wsURL, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))

	// Create SSH connection pair
	sshClient, sshServer := net.Pipe()
	defer sshClient.Close()
	defer sshServer.Close()

	// Track responses
	responses := make(chan []byte, 10)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := sshServer.Read(buf)
			if err != nil {
				close(responses)
				return
			}
			responses <- append([]byte(nil), buf[:n]...)
		}
	}()

	// Start the proxy
	go func() {

		if err := ProxySSHToWebSocket(ctx, sshClient, wsURL, ts); err != nil && ctx.Err() == nil {
			t.Errorf("ProxySSHToWebSocket failed: %v", err)
		}
	}()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// Send multiple messages
	testMessages := []string{"msg1", "msg2", "msg3"}
	for _, msg := range testMessages {
		_, err := sshServer.Write([]byte(msg))
		if err != nil {
			t.Fatalf("Failed to write message %q: %v", msg, err)
		}
	}

	// Verify echo responses
	for i, expectedMsg := range testMessages {
		select {
		case response := <-responses:
			expected := "echo: " + expectedMsg
			if string(response) != expected {
				t.Errorf("Message %d: expected %q, got %q", i, expected, string(response))
			}
		case <-time.After(1 * time.Second):
			t.Errorf("Timeout waiting for response to message %d", i)
		}
	}
}

func TestProxySSHToWebSocket_LargeData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create mock WebSocket server
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	var receivedData []byte
	var dataMutex sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			dataMutex.Lock()
			receivedData = append(receivedData, message...)
			dataMutex.Unlock()
		}
	}))
	defer server.Close()

	wsURL, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http"))

	// Create SSH connection pair
	sshClient, sshServer := net.Pipe()
	defer sshClient.Close()
	defer sshServer.Close()

	// Start the proxy
	go func() {
		if err := ProxySSHToWebSocket(ctx, sshClient, wsURL, ts); err != nil && ctx.Err() == nil {
			t.Errorf("ProxySSHToWebSocket failed: %v", err)
		}
	}()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// Send large data (50KB)
	largeData := make([]byte, 50*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	if _, err := sshServer.Write(largeData); err != nil {
		t.Fatalf("Failed to write large data: %v", err)
	}

	// Wait for all data to be received
	time.Sleep(500 * time.Millisecond)

	dataMutex.Lock()
	if len(receivedData) != len(largeData) {
		t.Errorf("Large data: expected %d bytes, got %d bytes", len(largeData), len(receivedData))
	}
	if d := cmp.Diff(largeData, receivedData); d != "" {
		t.Errorf("Data mismatch (-want +got):\n%s", d)
	}
	dataMutex.Unlock()
}
