package grpctransport

import (
	"context"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
	sdkv1grpc "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1grpc"
)

// host_server.go — the host-side sdkv1 adapter. The host serves this on the
// go-plugin broker; each sdkv1 request is converted to the native
// sdk.HostService contract (backed by *sdk.HostServiceServer, which owns
// identity/auth/rate-limiting) and each result back to sdkv1.
type grpcHostServiceServer struct {
	host sdk.HostService
}

var _ sdkv1grpc.HostServiceServer = (*grpcHostServiceServer)(nil)

func (s *grpcHostServiceServer) CallAction(ctx context.Context, req *sdkv1.CallActionRequest) (*sdkv1.CallActionResponse, error) {
	params, err := unmarshalMapE(req.GetParamsJson())
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgument, "params_json decode failed: %v", err)
	}
	result, err := s.host.CallAction(ctx, req.GetPlatform(), req.GetApi(), params)
	if err != nil {
		return nil, err
	}
	return &sdkv1.CallActionResponse{ResultJson: marshalJSON(result)}, nil
}

func (s *grpcHostServiceServer) SendMessage(ctx context.Context, req *sdkv1.SendMessageRequest) (*sdkv1.Empty, error) {
	// 原生组件链可携带 BinaryPayload 大文件（file 型 blob 句柄）：经宿主
	// ReadBlob 还原为内联 Base64 后再交给原生 SendMessage。
	chain, err := protoToComponentsWithBlob(req.GetChainComponents(), func(handleID string) ([]byte, error) {
		return readBlobAll(ctx, s.host, handleID)
	})
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgument, "chain component decode failed: %v", err)
	}
	if err := s.host.SendMessage(ctx, req.GetPlatform(), req.GetSessionId(), chain); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

// readBlobAll 经原生 HostService.ReadBlob 分块读回整个 blob。
func readBlobAll(ctx context.Context, h sdk.HostService, handleID string) ([]byte, error) {
	var out []byte
	var offset int64
	for {
		chunk, eof, _, err := h.ReadBlob(ctx, handleID, offset, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		offset += int64(len(chunk))
		if eof {
			return out, nil
		}
		if len(chunk) == 0 {
			return out, nil
		}
	}
}

func (s *grpcHostServiceServer) RecallMessage(ctx context.Context, req *sdkv1.RecallMessageRequest) (*sdkv1.Empty, error) {
	if err := s.host.RecallMessage(ctx, req.GetPlatform(), req.GetMessageId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) GetConfig(ctx context.Context, req *sdkv1.GetConfigRequest) (*sdkv1.GetConfigResponse, error) {
	cfg, err := s.host.GetConfig(ctx, req.GetPluginName())
	if err != nil {
		return nil, err
	}
	return &sdkv1.GetConfigResponse{ConfigJson: marshalJSON(cfg)}, nil
}

func (s *grpcHostServiceServer) SetConfig(ctx context.Context, req *sdkv1.SetConfigRequest) (*sdkv1.Empty, error) {
	if err := s.host.SetConfig(ctx, req.GetPluginName(), unmarshalMap(req.GetConfigJson())); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) ChatLLM(ctx context.Context, req *sdkv1.ChatLLMRequest) (*sdkv1.ChatLLMResponse, error) {
	text, err := s.host.ChatLLM(ctx, protoToHostChatLLM(req))
	if err != nil {
		return nil, err
	}
	return &sdkv1.ChatLLMResponse{Text: text}, nil
}

func (s *grpcHostServiceServer) React(ctx context.Context, req *sdkv1.ReactRequest) (*sdkv1.Empty, error) {
	if err := s.host.React(ctx, req.GetPlatform(), req.GetSessionId(), req.GetMessageId(), req.GetEmoji()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) TextToImage(ctx context.Context, req *sdkv1.TextToImageRequest) (*sdkv1.TextToImageResponse, error) {
	b64, err := s.host.TextToImage(ctx, req.GetText(), req.GetTemplateName())
	if err != nil {
		return nil, err
	}
	return dualImageResponse(b64), nil
}

func (s *grpcHostServiceServer) HtmlRender(ctx context.Context, req *sdkv1.HtmlRenderRequest) (*sdkv1.HtmlRenderResponse, error) {
	b64, err := s.host.HtmlRender(ctx, req.GetTemplate(), req.GetData(), req.GetOptions())
	if err != nil {
		return nil, err
	}
	return dualHtmlResponse(b64), nil
}

func (s *grpcHostServiceServer) GetCurrConversationID(ctx context.Context, req *sdkv1.ConversationIDRequest) (*sdkv1.ConversationIDResponse, error) {
	return &sdkv1.ConversationIDResponse{Cid: s.host.GetCurrConversationID(ctx, req.GetUnifiedMsgOrigin())}, nil
}

func (s *grpcHostServiceServer) NewConversation(ctx context.Context, req *sdkv1.NewConversationRequest) (*sdkv1.ConversationIDResponse, error) {
	return &sdkv1.ConversationIDResponse{Cid: s.host.NewConversation(ctx, req.GetUnifiedMsgOrigin(), req.GetPlatformId(), req.GetPersonaId())}, nil
}

func (s *grpcHostServiceServer) GetConversation(ctx context.Context, req *sdkv1.GetConversationRequest) (*sdkv1.ConversationResponse, error) {
	return &sdkv1.ConversationResponse{ConversationJson: marshalJSON(s.host.GetConversation(ctx, req.GetUnifiedMsgOrigin(), req.GetConversationId(), req.GetCreateIfNotExists()))}, nil
}

func (s *grpcHostServiceServer) GetConversations(ctx context.Context, req *sdkv1.GetConversationsRequest) (*sdkv1.ConversationsResponse, error) {
	resp := &sdkv1.ConversationsResponse{}
	for _, c := range s.host.GetConversations(ctx, req.GetUnifiedMsgOrigin()) {
		resp.ConversationsJson = append(resp.ConversationsJson, marshalJSON(c))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) DeleteConversation(ctx context.Context, req *sdkv1.DeleteConversationRequest) (*sdkv1.Empty, error) {
	if err := s.host.DeleteConversation(ctx, req.GetUnifiedMsgOrigin(), req.GetConversationId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) SwitchConversation(ctx context.Context, req *sdkv1.SwitchConversationRequest) (*sdkv1.Empty, error) {
	if err := s.host.SwitchConversation(ctx, req.GetUnifiedMsgOrigin(), req.GetConversationId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) UpdateConversationTitle(ctx context.Context, req *sdkv1.UpdateConversationTitleRequest) (*sdkv1.Empty, error) {
	if err := s.host.UpdateConversationTitle(ctx, req.GetUnifiedMsgOrigin(), req.GetConversationId(), req.GetTitle()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) UpdateConversationPersonaID(ctx context.Context, req *sdkv1.UpdateConversationPersonaRequest) (*sdkv1.Empty, error) {
	if err := s.host.UpdateConversationPersonaID(ctx, req.GetUnifiedMsgOrigin(), req.GetConversationId(), req.GetPersonaId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) GetPersonas(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.PersonasResponse, error) {
	resp := &sdkv1.PersonasResponse{}
	for _, p := range s.host.GetPersonas(ctx) {
		resp.PersonasJson = append(resp.PersonasJson, marshalJSON(p))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) GetDefaultPersona(ctx context.Context, req *sdkv1.GetDefaultPersonaRequest) (*sdkv1.PersonaResponse, error) {
	return &sdkv1.PersonaResponse{PersonaJson: marshalJSON(s.host.GetDefaultPersona(ctx, req.GetUmo()))}, nil
}

func (s *grpcHostServiceServer) GetPersonaTree(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.PersonaTreeResponse, error) {
	folders, personas := s.host.GetPersonaTree(ctx)
	resp := &sdkv1.PersonaTreeResponse{}
	for _, f := range folders {
		resp.FoldersJson = append(resp.FoldersJson, marshalJSON(f))
	}
	for _, p := range personas {
		resp.PersonasJson = append(resp.PersonasJson, marshalJSON(p))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) ResolveSelectedPersona(ctx context.Context, req *sdkv1.ResolvePersonaRequest) (*sdkv1.ResolvePersonaResponse, error) {
	personaID, personaName, personaPrompt, forceApplied, isDefault := s.host.ResolveSelectedPersona(
		ctx, req.GetUmo(), req.GetConversationPersonaId(), req.GetPlatformName(), unmarshalMap(req.GetProviderSettingsJson()),
	)
	return &sdkv1.ResolvePersonaResponse{
		PersonaId:             personaID,
		PersonaName:           personaName,
		PersonaPrompt:         personaPrompt,
		ForceAppliedPersonaId: forceApplied,
		IsDefault:             isDefault,
	}, nil
}

func (s *grpcHostServiceServer) ListProviders(ctx context.Context, req *sdkv1.ListProvidersRequest) (*sdkv1.ProvidersResponse, error) {
	resp := &sdkv1.ProvidersResponse{}
	for _, p := range s.host.ListProviders(ctx, req.GetCapability()) {
		resp.ProvidersJson = append(resp.ProvidersJson, marshalJSON(p))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) GetUsingProvider(ctx context.Context, req *sdkv1.GetUsingProviderRequest) (*sdkv1.ProviderResponse, error) {
	return &sdkv1.ProviderResponse{ProviderJson: marshalJSON(s.host.GetUsingProvider(ctx, req.GetUmo(), req.GetCapability()))}, nil
}

func (s *grpcHostServiceServer) SetProvider(ctx context.Context, req *sdkv1.SetProviderRequest) (*sdkv1.Empty, error) {
	if err := s.host.SetProvider(ctx, req.GetUmo(), req.GetProviderId(), req.GetCapability()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) GetProviderModels(ctx context.Context, req *sdkv1.GetProviderModelsRequest) (*sdkv1.ProviderModelsResponse, error) {
	return &sdkv1.ProviderModelsResponse{Models: s.host.GetProviderModels(ctx, req.GetProviderId())}, nil
}

func (s *grpcHostServiceServer) GetPluginRegistry(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.StarsResponse, error) {
	resp := &sdkv1.StarsResponse{}
	for _, st := range s.host.GetPluginRegistry(ctx) {
		resp.StarsJson = append(resp.StarsJson, marshalJSON(st))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) GetStar(ctx context.Context, req *sdkv1.GetStarRequest) (*sdkv1.StarResponse, error) {
	return &sdkv1.StarResponse{StarJson: marshalJSON(s.host.GetStar(ctx, req.GetName()))}, nil
}

func (s *grpcHostServiceServer) SetPluginEnabled(ctx context.Context, req *sdkv1.SetPluginEnabledRequest) (*sdkv1.Empty, error) {
	if err := s.host.SetPluginEnabled(ctx, req.GetPluginName(), req.GetEnabled()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) InstallPlugin(ctx context.Context, req *sdkv1.InstallPluginRequest) (*sdkv1.Empty, error) {
	if err := s.host.InstallPlugin(ctx, req.GetRepo()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) UninstallPlugin(ctx context.Context, req *sdkv1.UninstallPluginRequest) (*sdkv1.Empty, error) {
	if err := s.host.UninstallPlugin(ctx, req.GetPluginName()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) ListCommandDescriptors(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.CommandDescriptorsResponse, error) {
	resp := &sdkv1.CommandDescriptorsResponse{}
	for _, d := range s.host.ListCommandDescriptors(ctx) {
		resp.DescriptorsJson = append(resp.DescriptorsJson, marshalJSON(d))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) ListPlatforms(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.PlatformsResponse, error) {
	resp := &sdkv1.PlatformsResponse{}
	for _, p := range s.host.ListPlatforms(ctx) {
		resp.PlatformsJson = append(resp.PlatformsJson, marshalJSON(p))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) RegisterSessionWait(ctx context.Context, req *sdkv1.RegisterSessionWaitRequest) (*sdkv1.RegisterSessionWaitResponse, error) {
	return &sdkv1.RegisterSessionWaitResponse{WaitId: s.host.RegisterSessionWait(ctx, "", req.GetUmo(), req.GetTimeoutSeconds())}, nil
}

func (s *grpcHostServiceServer) UnregisterSessionWait(ctx context.Context, req *sdkv1.UnregisterSessionWaitRequest) (*sdkv1.Empty, error) {
	s.host.UnregisterSessionWait(ctx, req.GetWaitId())
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) RegisterBridgeHook(ctx context.Context, req *sdkv1.BridgeHookRequest) (*sdkv1.Empty, error) {
	if err := s.host.RegisterBridgeHook(ctx, "", req.GetHookName()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) UnregisterBridgeHook(ctx context.Context, req *sdkv1.BridgeHookRequest) (*sdkv1.Empty, error) {
	if err := s.host.UnregisterBridgeHook(ctx, "", req.GetHookName()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) CreateBlob(ctx context.Context, req *sdkv1.CreateBlobRequest) (*sdkv1.CreateBlobResponse, error) {
	ref, err := s.host.CreateBlob(ctx, req.GetData(), req.GetMimeType(), req.GetFilename(), req.GetTtlSeconds())
	if err != nil {
		return nil, err
	}
	return &sdkv1.CreateBlobResponse{File: fileRefToProto(ref)}, nil
}

func (s *grpcHostServiceServer) ReadBlob(ctx context.Context, req *sdkv1.ReadBlobRequest) (*sdkv1.ReadBlobResponse, error) {
	data, eof, total, err := s.host.ReadBlob(ctx, req.GetHandleId(), req.GetOffset(), req.GetLimit())
	if err != nil {
		return nil, err
	}
	return &sdkv1.ReadBlobResponse{Data: data, Eof: eof, TotalSize: total}, nil
}

func (s *grpcHostServiceServer) GetBlobInfo(ctx context.Context, req *sdkv1.GetBlobInfoRequest) (*sdkv1.GetBlobInfoResponse, error) {
	ref, err := s.host.GetBlobInfo(ctx, req.GetHandleId())
	if err != nil {
		return nil, err
	}
	return &sdkv1.GetBlobInfoResponse{File: fileRefToProto(ref)}, nil
}

func (s *grpcHostServiceServer) ReleaseBlob(ctx context.Context, req *sdkv1.ReleaseBlobRequest) (*sdkv1.Empty, error) {
	if err := s.host.ReleaseBlob(ctx, req.GetHandleId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) ListSkills(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.SkillsResponse, error) {
	resp := &sdkv1.SkillsResponse{}
	for _, x := range s.host.ListSkills(ctx) {
		resp.SkillsJson = append(resp.SkillsJson, marshalJSON(x))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) SetSkillActive(ctx context.Context, req *sdkv1.SetSkillActiveRequest) (*sdkv1.Empty, error) {
	if err := s.host.SetSkillActive(ctx, req.GetName(), req.GetActive()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) DeleteSkill(ctx context.Context, req *sdkv1.DeleteSkillRequest) (*sdkv1.Empty, error) {
	if err := s.host.DeleteSkill(ctx, req.GetName()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) GetPlatformMessageHistory(ctx context.Context, req *sdkv1.GetPMHistoryRequest) (*sdkv1.PMHistoryRecordsResponse, error) {
	resp := &sdkv1.PMHistoryRecordsResponse{}
	for _, r := range s.host.GetPlatformMessageHistory(ctx, req.GetPlatformId(), req.GetUserId(), req.GetLimit()) {
		resp.RecordsJson = append(resp.RecordsJson, marshalJSON(r))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) InsertPlatformMessageHistory(ctx context.Context, req *sdkv1.InsertPMHistoryRequest) (*sdkv1.PMHistoryRecordResponse, error) {
	r := s.host.InsertPlatformMessageHistory(ctx, req.GetPlatformId(), req.GetUserId(), req.GetSenderId(), unmarshalAny(req.GetContentJson()), req.GetLlmCheckpointId(), req.GetMaxMessages())
	return &sdkv1.PMHistoryRecordResponse{RecordJson: marshalJSON(r)}, nil
}

func (s *grpcHostServiceServer) UpdatePlatformMessageHistory(ctx context.Context, req *sdkv1.UpdatePMHistoryRequest) (*sdkv1.Empty, error) {
	if err := s.host.UpdatePlatformMessageHistory(ctx, req.GetId(), unmarshalAny(req.GetContentJson()), req.GetLlmCheckpointId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) DeletePlatformMessageHistory(ctx context.Context, req *sdkv1.DeletePMHistoryRequest) (*sdkv1.Empty, error) {
	if err := s.host.DeletePlatformMessageHistory(ctx, req.GetId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) ListSkillsV2(ctx context.Context, req *sdkv1.ListSkillsV2Request) (*sdkv1.SkillsResponse, error) {
	resp := &sdkv1.SkillsResponse{}
	for _, x := range s.host.ListSkillsV2(ctx, req.GetActiveOnly(), req.GetRuntime(), req.GetShowSandboxPath()) {
		resp.SkillsJson = append(resp.SkillsJson, marshalJSON(x))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) KBRetrieve(ctx context.Context, req *sdkv1.KBRetrieveRequest) (*sdkv1.KBRetrieveResponse, error) {
	contextText, resultsJSON, err := s.host.KBRetrieve(ctx, req.GetQuery(), req.GetKbNames(), int(req.GetTopKFusion()), int(req.GetTopMFinal()))
	if err != nil {
		return nil, err
	}
	return &sdkv1.KBRetrieveResponse{ContextText: contextText, ResultsJson: resultsJSON}, nil
}

func (s *grpcHostServiceServer) KBUploadFromURL(ctx context.Context, req *sdkv1.KBUploadFromURLRequest) (*sdkv1.Empty, error) {
	if err := s.host.KBUploadFromURL(ctx, req.GetKbId(), req.GetUrl(), int(req.GetChunkSize()), int(req.GetChunkOverlap())); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) KBListKBs(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.KBListResponse, error) {
	resp := &sdkv1.KBListResponse{}
	for _, k := range s.host.KBListKBs(ctx) {
		resp.KbsJson = append(resp.KbsJson, marshalJSON(k))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) RegisterFileToken(ctx context.Context, req *sdkv1.RegisterFileTokenRequest) (*sdkv1.RegisterFileTokenResponse, error) {
	token, err := s.host.RegisterFileToken(ctx, req.GetPath(), req.GetTimeoutSec())
	if err != nil {
		return nil, err
	}
	return &sdkv1.RegisterFileTokenResponse{Token: token}, nil
}

func (s *grpcHostServiceServer) CronCreate(ctx context.Context, req *sdkv1.CronCreateRequest) (*sdkv1.CronJobResponse, error) {
	payload, err := unmarshalMapE(req.GetPayloadJson())
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgument, "payload_json decode failed: %v", err)
	}
	job, err := s.host.CronCreate(ctx, &sdk.CronCreateSpec{
		Name:           req.GetName(),
		JobType:        req.GetJobType(),
		CronExpression: req.GetCronExpression(),
		Timezone:       req.GetTimezone(),
		Payload:        payload,
		Description:    req.GetDescription(),
		Enabled:        req.GetEnabled(),
		RunOnce:        req.GetRunOnce(),
		RunAt:          req.GetRunAt(),
	})
	if err != nil {
		return nil, err
	}
	return &sdkv1.CronJobResponse{JobJson: marshalJSON(job)}, nil
}

func (s *grpcHostServiceServer) CronUpdate(ctx context.Context, req *sdkv1.CronUpdateRequest) (*sdkv1.CronJobResponse, error) {
	fields, err := unmarshalMapE(req.GetFieldsJson())
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgument, "fields_json decode failed: %v", err)
	}
	job, err := s.host.CronUpdate(ctx, req.GetJobId(), fields)
	if err != nil {
		return nil, err
	}
	return &sdkv1.CronJobResponse{JobJson: marshalJSON(job)}, nil
}

func (s *grpcHostServiceServer) CronDelete(ctx context.Context, req *sdkv1.CronDeleteRequest) (*sdkv1.Empty, error) {
	if err := s.host.CronDelete(ctx, req.GetJobId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) CronList(ctx context.Context, req *sdkv1.CronListRequest) (*sdkv1.CronJobsResponse, error) {
	resp := &sdkv1.CronJobsResponse{}
	for _, j := range s.host.CronList(ctx, req.GetJobType()) {
		resp.JobsJson = append(resp.JobsJson, marshalJSON(j))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) CronRunNow(ctx context.Context, req *sdkv1.CronRunNowRequest) (*sdkv1.Empty, error) {
	if err := s.host.CronRunNow(ctx, req.GetJobId()); err != nil {
		return nil, err
	}
	return &sdkv1.Empty{}, nil
}

func (s *grpcHostServiceServer) McpListTools(ctx context.Context, _ *sdkv1.Empty) (*sdkv1.McpToolsResponse, error) {
	resp := &sdkv1.McpToolsResponse{}
	for _, t := range s.host.McpListTools(ctx) {
		resp.ToolsJson = append(resp.ToolsJson, marshalJSON(t))
	}
	return resp, nil
}

func (s *grpcHostServiceServer) McpCallTool(ctx context.Context, req *sdkv1.McpCallToolRequest) (*sdkv1.McpCallToolResponse, error) {
	args, err := unmarshalMapE(req.GetArgumentsJson())
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgument, "arguments_json decode failed: %v", err)
	}
	result, text, isError, err := s.host.McpCallTool(ctx, req.GetServer(), req.GetToolName(), args)
	if err != nil {
		return nil, err
	}
	return &sdkv1.McpCallToolResponse{ResultJson: marshalJSON(result), IsError: isError, Text: text}, nil
}
