package grpctransport

import (
	"context"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
	sdkv1grpc "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// plugin_server.go — the plugin-side sdkv1 adapter. It is the ONLY place the
// plugin process exposes sdkv1: every request is converted to the native
// sdk.PluginService contract and every result back to sdkv1.
type pluginServiceServer struct {
	svc sdk.PluginService
}

var _ sdkv1grpc.PluginServiceServer = (*pluginServiceServer)(nil)

func (s *pluginServiceServer) Register(ctx context.Context, req *sdkv1.RegisterRequest) (*sdkv1.RegisterResponse, error) {
	info, err := s.svc.Register(ctx, req.GetProtocolVersion())
	if err != nil {
		return nil, err
	}
	return pluginInfoToProto(info), nil
}

func (s *pluginServiceServer) HandleCommand(ctx context.Context, req *sdkv1.HandleCommandRequest) (*sdkv1.HandleCommandResponse, error) {
	res, err := s.svc.HandleCommand(ctx, req.GetName(), req.GetArgs(), SDKEventToEvent(req.GetEvent()))
	if err != nil {
		return nil, err
	}
	return &sdkv1.HandleCommandResponse{
		Text:   res.Text,
		Stop:   res.Result.StopPropagation,
		Sent:   res.Result.Sent,
		Result: eventResultToProto(res.Result),
		Chain:  componentsToProto(res.Chain),
	}, nil
}

func (s *pluginServiceServer) HandleFilter(ctx context.Context, req *sdkv1.HandleFilterRequest) (*sdkv1.HandleFilterResponse, error) {
	res, err := s.svc.HandleFilter(ctx, req.GetName(), SDKEventToEvent(req.GetEvent()))
	if err != nil {
		return nil, err
	}
	return &sdkv1.HandleFilterResponse{
		Allow:  res.Allow,
		Sent:   res.Result.Sent,
		Result: eventResultToProto(res.Result),
	}, nil
}

func (s *pluginServiceServer) HandleHook(ctx context.Context, req *sdkv1.HandleHookRequest) (*sdkv1.HookResponse, error) {
	res, err := s.svc.HandleHook(ctx, req.GetName(), SDKEventToEvent(req.GetEvent()), protoToComponents(req.GetChain()), req.GetPayloadJson())
	if err != nil {
		return nil, err
	}
	return &sdkv1.HookResponse{
		Stop:    res.Result.StopPropagation,
		Handled: res.Result.Handled,
		Sent:    res.Result.Sent,
		Result:  eventResultToProto(res.Result),
		Chain:   componentsToProto(res.Chain),
	}, nil
}

func (s *pluginServiceServer) HandleLLMRequest(ctx context.Context, req *sdkv1.HandleLLMRequestRequest) (*sdkv1.HandleLLMRequestResponse, error) {
	res, err := s.svc.HandleLLMRequest(ctx, req.GetName(), SDKEventToEvent(req.GetEvent()), req.GetSystemPrompt(), req.GetUserPrompt())
	if err != nil {
		return nil, err
	}
	return &sdkv1.HandleLLMRequestResponse{
		SystemPrompt: res.SystemPrompt,
		UserPrompt:   res.UserPrompt,
		Stop:         res.Stop,
		Sent:         res.Result.Sent,
		Result:       eventResultToProto(res.Result),
	}, nil
}

func (s *pluginServiceServer) HandleTool(ctx context.Context, req *sdkv1.HandleToolRequest) (*sdkv1.HandleToolResponse, error) {
	res, err := s.svc.HandleTool(ctx, req.GetName(), unmarshalMap(req.GetArgsJson()), SDKEventToEvent(req.GetEvent()))
	if err != nil {
		return nil, err
	}
	return &sdkv1.HandleToolResponse{
		Text:    res.Text,
		IsError: res.IsError,
		Sent:    res.Result.Sent,
		Result:  eventResultToProto(res.Result),
	}, nil
}

func (s *pluginServiceServer) ListTools(ctx context.Context, _ *sdkv1.PluginRef) (*sdkv1.ListToolsResponse, error) {
	list, err := s.svc.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	resp := &sdkv1.ListToolsResponse{}
	for _, t := range list {
		resp.Tools = append(resp.Tools, toolDescToProto(t))
	}
	return resp, nil
}

func (s *pluginServiceServer) ListWebApis(ctx context.Context, _ *sdkv1.PluginRef) (*sdkv1.ListWebApisResponse, error) {
	list, err := s.svc.ListWebApis(ctx)
	if err != nil {
		return nil, err
	}
	resp := &sdkv1.ListWebApisResponse{}
	for _, w := range list {
		resp.WebApis = append(resp.WebApis, webAPIDescToProto(w))
	}
	return resp, nil
}

func (s *pluginServiceServer) HandleWebRequest(ctx context.Context, req *sdkv1.HandleWebRequestRequest) (*sdkv1.HandleWebRequestResponse, error) {
	res, err := s.svc.HandleWebRequest(ctx, sdk.HandleWebRequest{
		PluginID: req.GetPluginId(),
		Method:   req.GetMethod(),
		Path:     req.GetPath(),
		Query:    webKVToMap(req.GetQuery()),
		Headers:  webKVToMap(req.GetHeaders()),
		Body:     req.GetBody(),
		Files:    webUploadFilesFromProto(req.GetFiles()),
	})
	if err != nil {
		return nil, err
	}
	return &sdkv1.HandleWebRequestResponse{
		StatusCode: int32(res.StatusCode),
		Headers:    strMapToWebKV(res.Headers),
		Body:       res.Body,
	}, nil
}

func (s *pluginServiceServer) HealthCheck(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.HealthResponse, error) {
	info, err := s.svc.HealthCheck(ctx)
	if err != nil {
		return nil, err
	}
	out := &sdkv1.HealthResponse{
		Ok:               info.OK,
		Load:             info.Load,
		Version:          info.Version,
		RuntimeHeartbeat: info.RuntimeHeartbeat,
	}
	for _, ps := range info.Plugins {
		out.Plugins = append(out.Plugins, &sdkv1.PluginStatus{
			PluginId:     ps.PluginID,
			PluginName:   ps.PluginName,
			State:        ps.State,
			Health:       ps.Health,
			LastActivity: ps.LastActivity,
			Error:        ps.Error,
			Generation:   ps.Generation,
		})
	}
	return out, nil
}

func (s *pluginServiceServer) SetLogLevel(ctx context.Context, req *sdkv1.SetLogLevelRequest) (*sdkv1.Empty, error) {
	if err := s.svc.SetLogLevel(ctx, req.GetLevel()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *pluginServiceServer) FeedSessionWait(ctx context.Context, req *sdkv1.FeedSessionWaitRequest) (*sdkv1.FeedSessionWaitResponse, error) {
	res, err := s.svc.FeedSessionWait(ctx, SDKEventToEvent(req.GetEvent()))
	if err != nil {
		return nil, err
	}
	return &sdkv1.FeedSessionWaitResponse{Handled: res.Handled}, nil
}

func (s *pluginServiceServer) GetConfigSchema(ctx context.Context, _ *sdkv1.PluginRef) (*sdkv1.GetConfigSchemaResponse, error) {
	schema, err := s.svc.GetConfigSchema(ctx)
	if err != nil {
		return nil, err
	}
	return &sdkv1.GetConfigSchemaResponse{SchemaJson: schema}, nil
}

func (s *pluginServiceServer) Cleanup(ctx context.Context, _ *sdkv1.PluginRef) (*sdkv1.Empty, error) {
	if err := s.svc.Cleanup(ctx); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// FeedCronJob / ManagePlugin are python-shared runtime control calls; the Go
// PluginService does not implement them.
func (s *pluginServiceServer) FeedCronJob(context.Context, *sdkv1.FeedCronJobRequest) (*sdkv1.FeedCronJobResponse, error) {
	return nil, status.Error(codes.Unimplemented, "FeedCronJob not implemented by Go plugin service")
}

func (s *pluginServiceServer) ManagePlugin(context.Context, *sdkv1.ManagePluginRequest) (*sdkv1.ManagePluginResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ManagePlugin not implemented by Go plugin service")
}
