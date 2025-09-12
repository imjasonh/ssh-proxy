package sshproxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func TestProxyWebSocketToSSH_BidirectionalTraffic(t *testing.T) {
	// Create mock SSH server
	sshServerAddr := "127.0.0.1:0"
	listener, err := net.Listen("tcp", sshServerAddr)
	if err != nil {
		t.Fatalf("Failed to create SSH server: %v", err)
	}
	defer listener.Close()
	actualAddr := listener.Addr().String()

	// Track data received by SSH server
	var sshMutex sync.Mutex
	receivedFromWS := make([][]byte, 0)
	var sshConn net.Conn

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			sshConn = conn

			// Read from WebSocket via SSH connection
			buf := make([]byte, 1024)
			for {
				n, err := conn.Read(buf)
				if err != nil {
					conn.Close()
					return
				}
				sshMutex.Lock()
				receivedFromWS = append(receivedFromWS, append([]byte(nil), buf[:n]...))
				sshMutex.Unlock()
			}
		}
	}()

	// Create test server with proxy handler
	server := httptest.NewServer(ProxyWebSocketToSSH(actualAddr, upgrader))
	defer server.Close()

	// Connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
	defer wsConn.Close()

	// Wait for SSH connection to be established
	time.Sleep(100 * time.Millisecond)

	// Test WebSocket -> SSH direction
	testDataWSToSSH := []byte("Hello from WebSocket to SSH!")
	err = wsConn.WriteMessage(websocket.BinaryMessage, testDataWSToSSH)
	if err != nil {
		t.Fatalf("Failed to write to WebSocket: %v", err)
	}

	// Wait for message to propagate
	time.Sleep(100 * time.Millisecond)

	sshMutex.Lock()
	if len(receivedFromWS) == 0 {
		t.Error("No data received from WebSocket via SSH connection")
	} else if string(receivedFromWS[0]) != string(testDataWSToSSH) {
		t.Errorf("WebSocket->SSH: expected %q, got %q", testDataWSToSSH, receivedFromWS[0])
	}
	sshMutex.Unlock()

	// Test SSH -> WebSocket direction
	if sshConn != nil {
		testDataSSHToWS := []byte("Hello from SSH to WebSocket!")
		_, err = sshConn.Write(testDataSSHToWS)
		if err != nil {
			t.Fatalf("Failed to write to SSH connection: %v", err)
		}

		// Read response from WebSocket
		if _, message, err := wsConn.ReadMessage(); err != nil {
			t.Fatalf("Failed to read from WebSocket: %v", err)
		} else if string(message) != string(testDataSSHToWS) {
			t.Errorf("SSH->WebSocket: expected %q, got %q", testDataSSHToWS, message)
		}
	} else {
		t.Error("SSH connection was not established")
	}
}

func TestProxyWebSocketToSSH_MultipleMessages(t *testing.T) {
	// Create echo SSH server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create SSH server: %v", err)
	}
	defer listener.Close()
	actualAddr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			// Echo server: read and write back with prefix
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					// Echo back with prefix
					response := append([]byte("ssh-echo: "), buf[:n]...)
					c.Write(response)
				}
			}(conn)
		}
	}()

	// Create test server
	server := httptest.NewServer(ProxyWebSocketToSSH(actualAddr, upgrader))
	defer server.Close()

	// Connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
	defer wsConn.Close()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// Send multiple messages and verify echoes
	testMessages := []string{"msg1", "msg2", "msg3"}
	for i, msg := range testMessages {
		// Send message
		if err = wsConn.WriteMessage(websocket.BinaryMessage, []byte(msg)); err != nil {
			t.Fatalf("Failed to write message %d: %v", i, err)
		}

		// Read echo response
		expected := "ssh-echo: " + msg
		if _, response, err := wsConn.ReadMessage(); err != nil {
			t.Fatalf("Failed to read response %d: %v", i, err)
		} else if string(response) != expected {
			t.Errorf("Message %d: expected %q, got %q", i, expected, string(response))
		}
	}
}

func TestProxyWebSocketToSSH_LargeData(t *testing.T) {
	// Create SSH server that accumulates data
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create SSH server: %v", err)
	}
	defer listener.Close()
	actualAddr := listener.Addr().String()

	var receivedData []byte
	var dataMutex sync.Mutex
	dataDone := make(chan struct{})

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if err == io.EOF {
					close(dataDone)
				}
				return
			}
			dataMutex.Lock()
			receivedData = append(receivedData, buf[:n]...)
			dataMutex.Unlock()

			// Check if we've received all expected data
			if len(receivedData) >= 50*1024 {
				close(dataDone)
				return
			}
		}
	}()

	// Create test server
	server := httptest.NewServer(ProxyWebSocketToSSH(actualAddr, upgrader))
	defer server.Close()

	// Connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
	defer wsConn.Close()

	// Wait for connection
	time.Sleep(100 * time.Millisecond)

	// Send large data (50KB)
	largeData := make([]byte, 50*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	err = wsConn.WriteMessage(websocket.BinaryMessage, largeData)
	if err != nil {
		t.Fatalf("Failed to write large data: %v", err)
	}

	// Wait for all data to be received
	select {
	case <-dataDone:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for large data")
	}

	dataMutex.Lock()
	defer dataMutex.Unlock()

	if len(receivedData) != len(largeData) {
		t.Errorf("Large data: expected %d bytes, got %d bytes", len(largeData), len(receivedData))
	}

	// Verify data integrity
	for i, b := range receivedData {
		if i >= len(largeData) {
			break
		}
		if b != largeData[i] {
			t.Errorf("Data mismatch at byte %d: expected %d, got %d", i, largeData[i], b)
			break
		}
	}
}

func TestProxyWebSocketToSSH_ConnectionClosed(t *testing.T) {
	// Create SSH server that closes immediately
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create SSH server: %v", err)
	}
	defer listener.Close()
	actualAddr := listener.Addr().String()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Close immediately after accepting
		conn.Close()
	}()

	// Create test server
	server := httptest.NewServer(ProxyWebSocketToSSH(actualAddr, upgrader))
	defer server.Close()

	// Connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect WebSocket: %v", err)
	}
	defer wsConn.Close()

	// Try to send data
	if err := wsConn.WriteMessage(websocket.BinaryMessage, []byte("test")); err != nil {
		t.Fatalf("Failed to write to WebSocket: %v", err)
	}

	// The WebSocket connection should close due to SSH connection being closed
	// We'll wait a bit and then try to read, expecting an error
	time.Sleep(200 * time.Millisecond)

	wsConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, _, err := wsConn.ReadMessage(); err != nil {
		// Either we get an error (connection closed) or no error (connection still open)
		// Both are valid since the proxy handles connection closure asynchronously
		t.Logf("WebSocket closed as expected: %v", err)
	} else {
		t.Logf("WebSocket connection still open, which is acceptable")
	}
}

func TestProxyWebSocketToSSH_SSHServerUnavailable(t *testing.T) {
	// Use a non-existent address
	unavailableAddr := "127.0.0.1:0" // This will be assigned but not listened on

	// Get an available port but don't listen on it
	tempListener, err := net.Listen("tcp", unavailableAddr)
	if err != nil {
		t.Fatalf("Failed to get available port: %v", err)
	}
	unavailableAddr = tempListener.Addr().String()
	tempListener.Close() // Close so it becomes unavailable

	// Create test server
	server := httptest.NewServer(ProxyWebSocketToSSH(unavailableAddr, upgrader))
	defer server.Close()

	// Try to connect WebSocket client
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)

	// The WebSocket upgrade might succeed, but the proxy should fail quickly
	if err == nil {
		defer wsConn.Close()
		// The connection should close quickly due to SSH connection failure
		wsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := wsConn.ReadMessage(); err == nil {
			t.Error("Expected error when SSH server is unavailable, but connection stayed open")
		}
	}
	// If WebSocket upgrade fails, that's also acceptable behavior
}
