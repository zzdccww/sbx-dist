package main

import (
	"context"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/protocol/block"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/vless"
	"github.com/sagernet/sing-box/protocol/vmess"
	"github.com/sagernet/sing-box/protocol/wireguard"
)

// registryContext injects the protocol registries sing-box needs to decode a
// config. sing-box v1.14 no longer self-registers protocols via init(); the
// registries must be built explicitly and attached to the context before
// option.Options.UnmarshalJSONContext or box.New is called.
//
// Only the protocols this launcher actually emits are registered — see
// generateSingBoxConfig in the Java/Node.js/Python entrypoints.
func registryContext(ctx context.Context) context.Context {
	return box.Context(
		ctx,
		inboundRegistry(),
		outboundRegistry(),
		endpointRegistry(),
		dnsTransportRegistry(),
		service.NewRegistry(),
		certificate.NewRegistry(),
	)
}

// inboundRegistry registers VMess (Argo WS), VLESS (Reality) and Hysteria2.
func inboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()
	direct.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	vless.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
	return registry
}

// outboundRegistry registers direct/block plus the selector and urltest groups
// used by the WARP routing rules.
func outboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()
	direct.RegisterOutbound(registry)
	block.RegisterOutbound(registry)
	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)
	return registry
}

// endpointRegistry registers the WireGuard endpoint backing WARP_MODE.
func endpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()
	wireguard.RegisterEndpoint(registry)
	return registry
}

func dnsTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()
	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	local.RegisterTransport(registry)
	return registry
}
