# SSH Proxy

A Go proxy server for proxying incoming SSH connections to backend services over WebSockets, designed for Cloud Run deployments.

## Overview

SSH Proxy provides two main components:

1. **SSH Proxy Server** - Forwards SSH connections to a WebSocket backend
2. **Websocket Wrapper Package** - Wraps an existing SSH server implementation in a WebSocket façade, to receive WebSocket requests.

This allows you to deploy SSH services on platforms that don't support raw TCP connections (like Cloud Run) by tunneling SSH over WebSocket/HTTP.

## Usage

### Wrap your SSH server implementation as a WebSocket server.

```go
import (
    "golang.org/x/crypto/ssh"
    sshproxy "github.com/imjasonh/ssh-proxy"
)

// Create WebSocket wrapper
wsServer := sshproxy.NewWebSocketServer(&ssh.ServerConfig{
    PublicKeyCallback: yourAuthFunction,
}, func(channel ssh.Channel, requests <-chan *ssh.Request, perms *ssh.Permissions) {
    // Handle SSH session
})

// Serve over HTTP
http.Handle("/ssh", wsServer)
http.ListenAndServe(":8080", nil)
```

### SSH Proxy Server (for external access)

Use the provided `ssh-proxy` command locally, or integrate into your own application:

```bash
# Set environment variables
export WEBSOCKET_URL="wss://your-service.run.app/ssh"
export SSH_ADDR=":2222"

# Run the proxy
ssh-proxy

# Connect via SSH
ssh user@localhost -p 2222
```

You can run this server in GKE Autopilot behind a GCLB load balancer, and expose SSH servers running on Cloud Run.
## Authentication

The SSH proxy supports Google Cloud identity token authentication for secure WebSocket connections to Cloud Run services, so the Cloud Run service can be configured to only accept requests from the proxy.

## Examples

See the `cmd/ssh-proxy` directory for a complete example implementation.
