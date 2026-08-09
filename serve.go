package sdk

import (
	"context"
	"encoding/json"
	"os"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// Handshake is the go-plugin handshake shared between the host and plugins.
// It must match exactly on both sides (defined here once, used by both).
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ASTRBOT_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "astrbot-go-plugin-1",
}

// PluginMap is the go-plugin plugin map (single named service). Used by the
// HOST as the client-side map when launching a plugin process.
var PluginMap = map[string]plugin.Plugin{
	"plugin_service": &PluginServiceGRPCPlugin{},
}

// Serve runs the plugin's main loop: it registers with go-plugin and blocks
// until the host terminates the process. Call this from main().
func Serve(p *Plugin) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "astrbot-plugin." + p.Name,
		Level:  hclog.Info,
		Output: os.Stderr,
	})
	plugins := map[string]plugin.Plugin{
		"plugin_service": &PluginServiceGRPCPlugin{Impl: p},
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         plugins,
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          logger,
	})
}

// PluginServiceGRPCPlugin implements go-plugin's GRPCPlugin for the plugin
// service. The host obtains a *Client from it via GRPCClient.
type PluginServiceGRPCPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl *Plugin
}

// GRPCServer registers the PluginService on the given gRPC server.
func (p *PluginServiceGRPCPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	sdkv1.RegisterPluginServiceServer(s, &serviceServer{impl: p.Impl})
	return nil
}

// GRPCClient wraps the gRPC connection in a typed *Client for the host.
func (p *PluginServiceGRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return NewClient(c), nil
}

// serviceServer implements sdkv1.PluginServiceServer, dispatching RPCs to the
// plugin author's declared handlers.
type serviceServer struct {
	sdkv1.UnimplementedPluginServiceServer
	impl *Plugin
}

// Register returns the plugin's metadata and handler descriptors.
func (s *serviceServer) Register(context.Context, *sdkv1.RegisterRequest) (*sdkv1.RegisterResponse, error) {
	if s.impl == nil {
		return &sdkv1.RegisterResponse{}, nil
	}
	schema, err := json.Marshal(s.impl.ConfigSchema)
	if err != nil {
		schema = []byte("{}")
	}
	resp := &sdkv1.RegisterResponse{
		Name:              s.impl.Name,
		Version:           s.impl.Version,
		Description:       s.impl.Description,
		Author:            s.impl.Author,
		ConfigSchemaJson:  schema,
	}
	for _, c := range s.impl.Commands {
		resp.Commands = append(resp.Commands, &sdkv1.CommandDesc{
			Name:        c.Name,
			Aliases:     c.Aliases,
			Description: c.Description,
			Usage:       c.Usage,
			Permission:  c.Permission,
		})
	}
	for _, f := range s.impl.Filters {
		resp.Filters = append(resp.Filters, &sdkv1.FilterDesc{Name: f.Name})
	}
	for _, h := range s.impl.Hooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: h.Event})
	}
	return resp, nil
}

// HandleCommand dispatches to a command handler by name.
func (s *serviceServer) HandleCommand(_ context.Context, req *sdkv1.HandleCommandRequest) (*sdkv1.HandleCommandResponse, error) {
	if s.impl == nil {
		return &sdkv1.HandleCommandResponse{}, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, c := range s.impl.Commands {
		if c.Name != req.Name {
			continue
		}
		if c.Handler == nil {
			return &sdkv1.HandleCommandResponse{}, nil
		}
		text, err := c.Handler(e, req.Args)
		if err != nil {
			return nil, err
		}
		return &sdkv1.HandleCommandResponse{Text: text}, nil
	}
	return &sdkv1.HandleCommandResponse{}, nil
}

// HandleFilter dispatches to a filter handler by name.
func (s *serviceServer) HandleFilter(_ context.Context, req *sdkv1.HandleFilterRequest) (*sdkv1.HandleFilterResponse, error) {
	if s.impl == nil {
		return &sdkv1.HandleFilterResponse{Allow: true}, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, f := range s.impl.Filters {
		if f.Name != req.Name {
			continue
		}
		if f.Handler == nil {
			return &sdkv1.HandleFilterResponse{Allow: true}, nil
		}
		return &sdkv1.HandleFilterResponse{Allow: f.Handler(e)}, nil
	}
	return &sdkv1.HandleFilterResponse{Allow: true}, nil
}

// HandleHook dispatches to a hook handler by name.
func (s *serviceServer) HandleHook(_ context.Context, req *sdkv1.HandleHookRequest) (*sdkv1.Empty, error) {
	if s.impl == nil {
		return &sdkv1.Empty{}, nil
	}
	e := eventFromJSON(req.EventJson)
	for _, h := range s.impl.Hooks {
		if h.Name != req.Name {
			continue
		}
		if h.Handler == nil {
			return &sdkv1.Empty{}, nil
		}
		return &sdkv1.Empty{}, h.Handler(e)
	}
	return &sdkv1.Empty{}, nil
}

// HealthCheck reports the plugin's liveness.
func (s *serviceServer) HealthCheck(context.Context, *sdkv1.Empty) (*sdkv1.HealthResponse, error) {
	resp := &sdkv1.HealthResponse{Ok: true}
	if s.impl != nil {
		resp.Version = s.impl.Version
	}
	return resp, nil
}

// Cleanup invokes the plugin's OnUnload hook.
func (s *serviceServer) Cleanup(context.Context, *sdkv1.Empty) (*sdkv1.Empty, error) {
	if s.impl != nil && s.impl.OnUnload != nil {
		return &sdkv1.Empty{}, s.impl.OnUnload()
	}
	return &sdkv1.Empty{}, nil
}

// eventFromJSON decodes a serialized Event, tolerating empty/invalid payloads.
func eventFromJSON(b []byte) *Event {
	if len(b) == 0 {
		return &Event{}
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return &Event{}
	}
	return &e
}
