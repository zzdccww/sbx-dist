//go:build !tunnel_diagnostics

package main

import "github.com/sagernet/sing-box/log"

func newTunnelLogger(factory log.Factory, _ string) log.ContextLogger {
	return factory.NewLogger("cloudflared")
}

func recordTunnelProbe(bool) {}
