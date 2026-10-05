package grpctransport

import (
	"encoding/base64"
	"encoding/json"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
)

// convert.go — native DTO ⇄ sdkv1 的无状态转换助手，供 client.go /
// plugin_server.go / host_adapter.go / host_server.go 共用。

func eventResultToProto(r sdk.EventResult) *sdkv1.EventResult {
	return &sdkv1.EventResult{
		Handled:         r.Handled,
		Sent:            r.Sent,
		StopPropagation: r.StopPropagation,
	}
}

func protoToEventResult(r *sdkv1.EventResult) sdk.EventResult {
	if r == nil {
		return sdk.EventResult{}
	}
	return sdk.EventResult{
		Handled:         r.Handled,
		Sent:            r.Sent,
		StopPropagation: r.StopPropagation,
	}
}

func toolDescToProto(t sdk.ToolDesc) *sdkv1.ToolDesc {
	return &sdkv1.ToolDesc{
		Name:        t.Name,
		Description: t.Description,
		ParamsJson:  t.ParamsSchemaJSON,
	}
}

func protoToToolDesc(t *sdkv1.ToolDesc) sdk.ToolDesc {
	if t == nil {
		return sdk.ToolDesc{}
	}
	return sdk.ToolDesc{
		Name:             t.Name,
		Description:      t.Description,
		ParamsSchemaJSON: t.ParamsJson,
	}
}

func webAPIDescToProto(w sdk.WebAPIDesc) *sdkv1.WebApiDesc {
	return &sdkv1.WebApiDesc{
		Route:       w.Route,
		Methods:     w.Methods,
		Description: w.Desc,
	}
}

func protoToWebAPIDesc(w *sdkv1.WebApiDesc) sdk.WebAPIDesc {
	if w == nil {
		return sdk.WebAPIDesc{}
	}
	return sdk.WebAPIDesc{
		Route:   w.Route,
		Methods: w.Methods,
		Desc:    w.Description,
	}
}

func pluginInfoToProto(info sdk.PluginInfo) *sdkv1.RegisterResponse {
	resp := &sdkv1.RegisterResponse{
		Name:             info.Name,
		Version:          info.Version,
		Description:      info.Description,
		Author:           info.Author,
		ConfigSchemaJson: info.ConfigSchemaJSON,
		ProtocolVersion:  info.ProtocolVersion,
	}
	for _, c := range info.Commands {
		resp.Commands = append(resp.Commands, &sdkv1.CommandDesc{
			Name:         c.Name,
			Aliases:      c.Aliases,
			Description:  c.Description,
			Usage:        c.Usage,
			Permission:   c.Permission,
			ParentGroup:  c.ParentGroup,
			IsSubCommand: c.IsSubCommand,
		})
	}
	for _, f := range info.Filters {
		resp.Filters = append(resp.Filters, &sdkv1.FilterDesc{Name: f.Name})
	}
	for _, h := range info.Hooks {
		resp.Hooks = append(resp.Hooks, &sdkv1.HookDesc{Name: h.Name, Event: h.Event})
	}
	for _, t := range info.Tools {
		resp.Tools = append(resp.Tools, toolDescToProto(t))
	}
	for _, w := range info.WebAPIs {
		resp.WebApis = append(resp.WebApis, webAPIDescToProto(w))
	}
	return resp
}

func protoToPluginInfo(resp *sdkv1.RegisterResponse) sdk.PluginInfo {
	if resp == nil {
		return sdk.PluginInfo{}
	}
	info := sdk.PluginInfo{
		Name:             resp.Name,
		Version:          resp.Version,
		Description:      resp.Description,
		Author:           resp.Author,
		ConfigSchemaJSON: resp.ConfigSchemaJson,
		ProtocolVersion:  resp.ProtocolVersion,
	}
	for _, c := range resp.Commands {
		if c == nil {
			continue
		}
		info.Commands = append(info.Commands, sdk.CommandDesc{
			Name:         c.Name,
			Aliases:      c.Aliases,
			Description:  c.Description,
			Usage:        c.Usage,
			Permission:   c.Permission,
			ParentGroup:  c.ParentGroup,
			IsSubCommand: c.IsSubCommand,
		})
	}
	for _, f := range resp.Filters {
		if f == nil {
			continue
		}
		info.Filters = append(info.Filters, sdk.FilterDesc{Name: f.Name})
	}
	for _, h := range resp.Hooks {
		if h == nil {
			continue
		}
		info.Hooks = append(info.Hooks, sdk.HookDesc{Name: h.Name, Event: h.Event})
	}
	for _, t := range resp.Tools {
		info.Tools = append(info.Tools, protoToToolDesc(t))
	}
	for _, w := range resp.WebApis {
		info.WebAPIs = append(info.WebAPIs, protoToWebAPIDesc(w))
	}
	return info
}

func fileRefToProto(r *sdk.FileReference) *sdkv1.FileReference {
	if r == nil {
		return nil
	}
	return &sdkv1.FileReference{
		HandleId:  r.HandleID,
		Size:      r.Size,
		MimeType:  r.MimeType,
		Filename:  r.Filename,
		ExpiresAt: r.ExpiresAt,
	}
}

func protoToFileRef(r *sdkv1.FileReference) *sdk.FileReference {
	if r == nil {
		return nil
	}
	return &sdk.FileReference{
		HandleID:  r.HandleId,
		Size:      r.Size,
		MimeType:  r.MimeType,
		Filename:  r.Filename,
		ExpiresAt: r.ExpiresAt,
	}
}

func mapToWebKV(m map[string][]string) []*sdkv1.WebKV {
	if len(m) == 0 {
		return nil
	}
	out := make([]*sdkv1.WebKV, 0, len(m))
	for k, vs := range m {
		for _, v := range vs {
			out = append(out, &sdkv1.WebKV{Key: k, Value: v})
		}
	}
	return out
}

func webKVToMap(kvs []*sdkv1.WebKV) map[string][]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string][]string, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		out[kv.Key] = append(out[kv.Key], kv.Value)
	}
	return out
}

func strMapToWebKV(m map[string]string) []*sdkv1.WebKV {
	if len(m) == 0 {
		return nil
	}
	out := make([]*sdkv1.WebKV, 0, len(m))
	for k, v := range m {
		out = append(out, &sdkv1.WebKV{Key: k, Value: v})
	}
	return out
}

func webKVToStrMap(kvs []*sdkv1.WebKV) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		out[kv.Key] = kv.Value
	}
	return out
}

func hostChatLLMToProto(r *sdk.ChatLLMRequest) *sdkv1.ChatLLMRequest {
	if r == nil {
		return nil
	}
	return &sdkv1.ChatLLMRequest{
		Prompt:       r.Prompt,
		SystemPrompt: r.SystemPrompt,
		SessionId:    r.SessionID,
		ImageUrls:    r.ImageURLs,
		AudioUrls:    r.AudioURLs,
		ToolsJson:    r.ToolsJSON,
		ContextsJson: r.ContextsJSON,
		ProviderId:   r.ProviderID,
	}
}

func protoToHostChatLLM(r *sdkv1.ChatLLMRequest) *sdk.ChatLLMRequest {
	if r == nil {
		return nil
	}
	return &sdk.ChatLLMRequest{
		Prompt:       r.Prompt,
		SystemPrompt: r.SystemPrompt,
		SessionID:    r.SessionId,
		ImageURLs:    r.ImageUrls,
		AudioURLs:    r.AudioUrls,
		ToolsJSON:    r.ToolsJson,
		ContextsJSON: r.ContextsJson,
		ProviderID:   r.ProviderId,
	}
}

// imageBytesOrBase64 返回 base64 字符串：优先 proto 的 image_base64，缺失时
// 用 image_bytes 兜底编码（旧宿主只填 base64，新宿主两者都填）。
func imageBytesOrBase64(b64 string, raw []byte) string {
	if b64 != "" {
		return b64
	}
	if len(raw) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// dualImageResponse 双写 image 响应：保留 base64 string（旧 SDK 依赖）并解码
// 出原始 bytes 填 image_bytes（新 SDK 免一次 base64 往返）。TextToImage 是
// 低频慢操作，host 侧额外一次 base64 解码可忽略。
func dualImageResponse(b64 string) *sdkv1.TextToImageResponse {
	raw, _ := base64.StdEncoding.DecodeString(b64)
	return &sdkv1.TextToImageResponse{ImageBase64: b64, ImageBytes: raw}
}

// dualHtmlResponse 同 dualImageResponse，用于 HtmlRender 响应。
func dualHtmlResponse(b64 string) *sdkv1.HtmlRenderResponse {
	raw, _ := base64.StdEncoding.DecodeString(b64)
	return &sdkv1.HtmlRenderResponse{ImageBase64: b64, ImageBytes: raw}
}

// marshalJSON 是 map/any → JSON bytes 的薄封装；marshal 失败返回 nil（旧实现
// 直接返回 err，这里在无 error 返回的 native 接口下尽力降级）。
func marshalJSON(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// unmarshalAny 解析 JSON 到 any，空输入返回 nil。
func unmarshalAny(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}

// unmarshalMap 解析 JSON 对象到 map，空/非法输入返回空 map。
func unmarshalMap(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) == 0 {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// unmarshalMapE 同 unmarshalMap，但把非法 JSON 作为错误返回，供需要保留
// InvalidArgument 语义的调用方使用。
func unmarshalMapE(b []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(b) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// webUploadFilesToProto / webUploadFilesFromProto convert multipart file parts
// between the native and wire representations.
func webUploadFilesToProto(files []sdk.WebUploadFile) []*sdkv1.WebUploadFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]*sdkv1.WebUploadFile, 0, len(files))
	for _, f := range files {
		out = append(out, &sdkv1.WebUploadFile{
			Field:       f.Field,
			Filename:    f.Filename,
			ContentType: f.ContentType,
			Content:     f.Content,
		})
	}
	return out
}

func webUploadFilesFromProto(files []*sdkv1.WebUploadFile) []sdk.WebUploadFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]sdk.WebUploadFile, 0, len(files))
	for _, f := range files {
		out = append(out, sdk.WebUploadFile{
			Field:       f.GetField(),
			Filename:    f.GetFilename(),
			ContentType: f.GetContentType(),
			Content:     f.GetContent(),
		})
	}
	return out
}
