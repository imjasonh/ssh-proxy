# SSH Proxy

A Go library and proxy server for tunneling SSH connections over WebSockets, designed for Cloud Run apps that don't support raw TCP connections.

## Overview

SSH Proxy provides two main functions:

1. **SSH → WebSocket Proxy** - Accepts SSH connections and forwards them to a WebSocket backend on Cloud Run.
2. **WebSocket → SSH Proxy** - Accepts WebSocket connections and forwards them to a local SSH server implementation.

This allows you to deploy SSH services on Cloud Run by tunneling SSH traffic over HTTP/WebSocket connections.

## Usage

### SSH → WebSocket Proxy

Use the SSH proxy command to forward local SSH connections to a WebSocket endpoint.

You can run this in [GKE Autopilot](https://cloud.google.com/kubernetes-engine/docs/concepts/autopilot-overview) for a minimal, cost-effective and low-maintenance environment to proxy requests.

### WebSocket → SSH Proxy

Create a WebSocket handler that forwards connections to a local SSH server:

```go
import (
    "net/http"
    "github.com/gorilla/websocket"
    sshproxy "github.com/imjasonh/ssh-proxy"
)

upgrader := websocket.Upgrader{
    // Accept requests from all origins; consider changing this.
    CheckOrigin: func(r *http.Request) bool { return true },
}

// Forward WebSocket connections to local SSH server at :22
http.Handle("/ssh", sshproxy.ProxyWebSocketToSSH(":22", upgrader))
http.ListenAndServe(":8080", nil)
```

## Authentication

The SSH proxy supports Google Cloud identity token authentication for secure WebSocket connections. When connecting to Cloud Run services, the proxy automatically obtains and includes identity tokens in the Authorization header.

## SSH Host Key

The SSH server's host key is loaded from Google Cloud Secret Manager at startup.
