package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/chainguard-dev/clog"
	_ "github.com/chainguard-dev/clog/gcp/init"
	envconfig "github.com/sethvargo/go-envconfig"

	sshproxy "github.com/imjasonh/ssh-proxy"
)

var env = envconfig.MustProcess(context.Background(), &struct {
	WebsocketURL string `env:"WEBSOCKET_URL,required"`
	SSHAddr      string `env:"SSH_ADDR,default=:22"`
	Port         int    `env:"PORT" required:"true" default:"8080"`
}{})

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := sshproxy.NewProxy(env.WebsocketURL, env.SSHAddr).Start(ctx); err != nil {
		clog.FatalContext(ctx, "failed to start SSH proxy", "error", err)
	}
}
