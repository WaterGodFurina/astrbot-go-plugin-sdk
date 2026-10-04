// Package grpctransport is the gRPC + go-plugin transport for the SDK. It is
// imported by the host and by subprocess (gRPC) plugins, but never by Native
// plugins — that is what keeps google.golang.org/grpc and hashicorp/go-plugin
// out of Native builds.
package grpctransport

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk"
	sdkv1grpc "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1grpc"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Handshake is the go-plugin handshake shared between the host and plugins.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ASTRBOT_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "astrbot-go-plugin-1",
}

// PluginMap is the host-side go-plugin plugin map (single named service).
var PluginMap = map[string]plugin.Plugin{
	"plugin_service": &PluginServiceGRPCPlugin{},
}

// statusInterceptor maps core sdk coded errors to gRPC status codes so the host
// client keeps observing UNIMPLEMENTED / PERMISSION_DENIED etc. from code that
// no longer imports grpc.
func statusInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	if err != nil {
		var se *sdk.StatusError
		if errors.As(err, &se) {
			return resp, status.Error(codes.Code(se.Code), se.Msg)
		}
	}
	return resp, err
}

// grpcServer is the plugin-side gRPC server factory: raises the 4MB message cap
// and installs the core-error -> gRPC-status mapping.
func grpcServer(opts []grpc.ServerOption) *grpc.Server {
	opts = append(opts,
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
		grpc.ChainUnaryInterceptor(statusInterceptor),
	)
	return plugin.DefaultGRPCServer(opts)
}

// serve is the real sdk.Serve implementation (installed via init).
func serve(p *sdk.Plugin) {
	if p == nil {
		p = &sdk.Plugin{}
	}
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
		GRPCServer:      grpcServer,
		Logger:          logger,
	})
}

// PluginServiceGRPCPlugin implements go-plugin's GRPCPlugin.
type PluginServiceGRPCPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	Impl *sdk.Plugin
}

// GRPCServer registers the plugin's PluginService (the grpc-free core
// implementation) on the given gRPC server and captures the broker for
// plugin->host reverse calls.
func (p *PluginServiceGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	if broker != nil {
		setBroker(broker)
		// go-plugin 的 broker ConnInfo 只在宿主 accept 后约 5s 内有效，插件
		// 需尽快 Dial 并保持连接，否则反向调用会因 ConnInfo 过期而超时。
		go func() {
			for i := 0; i < 20; i++ {
				if _, err := dialBrokerHost(); err == nil {
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
			hclog.New(&hclog.LoggerOptions{Name: "astrbot-plugin", Level: hclog.Info, Output: os.Stderr}).
				Error("预连接宿主 HostService 失败，反向调用可能永久不可用")
		}()
	}
	sdkv1grpc.RegisterPluginServiceServer(s, sdk.NewPluginService(p.Impl))
	return nil
}

// GRPCClient wraps the connection in a typed *Client for the host and serves
// the HostService over the broker.
func (p *PluginServiceGRPCPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	client := NewClient(c)
	if broker != nil {
		srv, lis, server, err := acceptHostService(broker, sdk.HostServiceAppID)
		if err == nil {
			client.setHostServiceServer(srv, lis, server)
		} else {
			name := ""
			if p.Impl != nil {
				name = p.Impl.Name
			}
			hclog.New(&hclog.LoggerOptions{
				Name:   "astrbot-plugin." + name,
				Level:  hclog.Info,
				Output: os.Stderr,
			}).Warn("acceptHostService 失败：插件将无法反向调用宿主 HostService", "err", err)
		}
	}
	return client, nil
}

// acceptHostService serves HostService on a broker listener so plugins can dial
// back into the host.
func acceptHostService(b *plugin.GRPCBroker, id uint32) (*grpc.Server, net.Listener, *sdk.HostServiceServer, error) {
	lis, err := b.Accept(id)
	if err != nil {
		return nil, nil, nil, err
	}
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxGRPCMessageSize),
		grpc.MaxSendMsgSize(maxGRPCMessageSize),
		grpc.ChainUnaryInterceptor(statusInterceptor),
	)
	pid := sdk.CurrentHostPluginID()
	if pid == "" {
		sdk.HostServiceLogWarn("acceptHostService: 宿主未设置当前插件 id，控制面 RPC 将被拒绝")
	}
	server := sdk.NewHostServiceServer(pid, pid)
	sdkv1grpc.RegisterHostServiceServer(srv, server)
	go func() { _ = srv.Serve(lis) }()
	return srv, lis, server, nil
}

// ---------------------------------------------------------------------------
// Plugin-side: dialing the host's HostService over the go-plugin broker.
// ---------------------------------------------------------------------------

var (
	brokerMu sync.RWMutex
	broker   *plugin.GRPCBroker

	hostMu       sync.Mutex
	hostSvc      sdk.HostService
	hostConn     *grpc.ClientConn
	hostDialDone bool

	// hostDialMu serializes dialing without holding hostMu (a stuck Dial must
	// not queue every reverse caller).
	hostDialMu sync.Mutex
)

func setBroker(b *plugin.GRPCBroker) {
	brokerMu.Lock()
	broker = b
	hostMu.Lock()
	if hostConn != nil {
		_ = hostConn.Close()
	}
	hostConn = nil
	hostSvc = nil
	hostDialDone = false
	hostMu.Unlock()
	brokerMu.Unlock()
}

// dialBrokerHost returns the plugin->host caller over the go-plugin broker.
// Only successful dials are cached (a transient failure must not poison the
// plugin for its whole lifetime).
func dialBrokerHost() (sdk.HostService, error) {
	brokerMu.RLock()
	b := broker
	brokerMu.RUnlock()
	if b == nil {
		return nil, errors.New("host service unavailable: plugin not being served")
	}
	hostMu.Lock()
	if hostDialDone && hostSvc != nil {
		svc := hostSvc
		hostMu.Unlock()
		return svc, nil
	}
	hostMu.Unlock()

	hostDialMu.Lock()
	defer hostDialMu.Unlock()
	hostMu.Lock()
	if hostDialDone && hostSvc != nil {
		svc := hostSvc
		hostMu.Unlock()
		return svc, nil
	}
	hostMu.Unlock()

	conn, err := b.DialWithOptions(sdk.HostServiceAppID,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxGRPCMessageSize),
			grpc.MaxCallSendMsgSize(maxGRPCMessageSize),
		))
	if err != nil {
		return nil, err
	}
	svc := sdk.HostService(grpcHostCaller{c: sdkv1grpc.NewHostServiceClient(conn), opts: rpcCallOpts})
	hostMu.Lock()
	hostConn = conn
	hostSvc = svc
	hostDialDone = true
	hostMu.Unlock()
	return svc, nil
}

func init() {
	sdk.SetServeFunc(serve)
	sdk.SetHostCallerFunc(dialBrokerHost)
}
