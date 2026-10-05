// proto.go — SDKEvent ⇄ sdk.Event / proto Component ⇄ sdk.Component 转换
// （P1 native data plane）。这是 transport/grpc 里唯一直接触碰 sdkv1 的地方。
//
// 新协议下 Event / Message Chain 走 protobuf 原生（0 次 JSON）；仅动态
// metadata / Component.Data（Json 卡片）保留 JSON。二进制走 bytes / Blob。
package grpctransport

import (
	"encoding/base64"
	"encoding/json"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/v2/gen/sdkv1"
)

// EventToSDKEvent 把 sdk.Event（struct）转成 proto SDKEvent。固定字段直拷，
// Chain 走 repeated Component，仅 Metadata 做一次 JSON（动态结构）。
func EventToSDKEvent(e *sdk.Event) *sdkv1.SDKEvent {
	if e == nil {
		return nil
	}
	se := &sdkv1.SDKEvent{
		Type:        e.Type,
		Platform:    e.Platform,
		PlatformId:  e.PlatformID,
		MessageType: e.MessageType,
		SelfId:      e.SelfID,
		SenderId:    e.SenderID,
		SenderName:  e.SenderName,
		ConvId:      e.ConvID,
		GroupName:   e.GroupName,
		IsGroup:     e.IsGroup,
		IsAtBot:     e.IsAtBot,
		IsAdmin:     e.IsAdmin,
		MessageStr:  e.MessageStr,
		PlainText:   e.PlainText,
		RawMessage:  e.RawMessage,
		MessageId:   e.MessageID,
		Timestamp:   e.Timestamp,
	}
	if len(e.Chain) > 0 {
		se.Components = componentsToProto(e.Chain)
	}
	if len(e.Metadata) > 0 {
		if b, err := json.Marshal(e.Metadata); err == nil {
			se.MetadataJson = b
		}
	}
	return se
}

// SDKEventToEvent 把 proto SDKEvent 还原为 sdk.Event（固定字段直拷，metadata
// 一次 JSON 解析，Components 走 proto→struct）。
func SDKEventToEvent(se *sdkv1.SDKEvent) *sdk.Event {
	if se == nil {
		return nil
	}
	e := &sdk.Event{
		Type:        se.Type,
		Platform:    se.Platform,
		PlatformID:  se.PlatformId,
		MessageType: se.MessageType,
		SelfID:      se.SelfId,
		SenderID:    se.SenderId,
		SenderName:  se.SenderName,
		ConvID:      se.ConvId,
		GroupName:   se.GroupName,
		IsGroup:     se.IsGroup,
		IsAtBot:     se.IsAtBot,
		IsAdmin:     se.IsAdmin,
		MessageStr:  se.MessageStr,
		PlainText:   se.PlainText,
		RawMessage:  se.RawMessage,
		MessageID:   se.MessageId,
		Timestamp:   se.Timestamp,
	}
	if len(se.Components) > 0 {
		e.Chain = protoToComponents(se.Components)
	}
	if len(se.MetadataJson) > 0 {
		var m map[string]any
		if json.Unmarshal(se.MetadataJson, &m) == nil {
			e.Metadata = m
		}
	}
	return e
}

// componentsToProto 把 sdk.Component 切片转成 proto Component。
// 媒体 Base64 转 bytes base64_data；Json 卡片 Data 保留 data_json；
// Reply 引用消息携带 sender_*/chain（嵌套，带深度上限）。
func componentsToProto(chain []sdk.Component) []*sdkv1.Component {
	return componentsToProtoDepth(chain, 0)
}

// maxComponentDepth 与 Python SDK serialize.py 的 _MAX_NODE_DEPTH 对齐，
// 防御畸形自嵌套链（Reply/Forward）导致递归构造过深。
const maxComponentDepth = 50

func componentsToProtoDepth(chain []sdk.Component, depth int) []*sdkv1.Component {
	out := make([]*sdkv1.Component, 0, len(chain))
	for _, c := range chain {
		pc := &sdkv1.Component{
			Type:     string(c.Type),
			Text:     c.Text,
			TargetId: c.TargetID,
			Name:     c.Name,
			Url:      c.URL,
			Path:     c.Path,
			File:     c.File,
			FileId:   c.FileID,
			Id:       c.ID,
		}
		if c.Base64 != "" {
			if b, err := base64.StdEncoding.DecodeString(c.Base64); err == nil {
				pc.Base64Data = b
			}
		}
		if len(c.Data) > 0 {
			if b, err := json.Marshal(c.Data); err == nil {
				pc.DataJson = b
			}
		}
		if depth < maxComponentDepth && len(c.Chain) > 0 {
			pc.Chain = componentsToProtoDepth(c.Chain, depth+1)
		}
		pc.SenderId, pc.SenderName, pc.SenderTime = c.SenderID, c.SenderName, c.SenderTime
		out = append(out, pc)
	}
	return out
}

// protoToComponents 把 proto Component 还原为 sdk.Component。
// base64_data → Base64 string；data_json → Data map；Reply 引用消息还原
// sender_*/chain（嵌套递归）。file 型 payload（blob 句柄）不在此解析——需要
// blob store 的方向用 protoToComponentsWithBlob 传入 resolver。
func protoToComponents(comps []*sdkv1.Component) []sdk.Component {
	out, _ := protoToComponentsWithBlob(comps, nil)
	return out
}

// blobResolver 按 handleID 读回完整 blob（由 host 侧经 ReadBlob 提供）。
type blobResolver func(handleID string) ([]byte, error)

// protoToComponentsWithBlob 是 protoToComponents 的带 blob 解析版本：遇到
// BinaryPayload_File 时经 resolver 读回完整数据并内联为 Base64。resolver 为
// nil 时忽略 file 型 payload（插件收方向不出现该形态）。读 blob 失败时返回
// 错误（与旧 host 侧 SendMessage 的 fail 语义一致）。
func protoToComponentsWithBlob(comps []*sdkv1.Component, resolve blobResolver) ([]sdk.Component, error) {
	out := make([]sdk.Component, 0, len(comps))
	for _, c := range comps {
		if c == nil {
			continue
		}
		sc := sdk.Component{
			Type:       sdk.ComponentType(c.Type),
			Text:       c.Text,
			TargetID:   c.TargetId,
			Name:       c.Name,
			URL:        c.Url,
			Path:       c.Path,
			File:       c.File,
			FileID:     c.FileId,
			ID:         c.Id,
			SenderID:   c.SenderId,
			SenderName: c.SenderName,
			SenderTime: c.SenderTime,
		}
		if len(c.Chain) > 0 {
			sub, err := protoToComponentsWithBlob(c.Chain, resolve)
			if err != nil {
				return nil, err
			}
			sc.Chain = sub
		}
		if len(c.Base64Data) > 0 {
			sc.Base64 = base64.StdEncoding.EncodeToString(c.Base64Data)
		}
		if c.Payload != nil {
			switch p := c.Payload.Payload.(type) {
			case *sdkv1.BinaryPayload_InlineData:
				// 内联二进制无需宿主 blob store，插件侧可直接解码。
				sc.Base64 = base64.StdEncoding.EncodeToString(p.InlineData)
				sc.File = ""
			case *sdkv1.BinaryPayload_File:
				// file 型 payload 依赖宿主 blob store，由 host 侧经 ReadBlob 还原。
				if resolve != nil && p.File != nil {
					b, err := resolve(p.File.HandleId)
					if err != nil {
						return nil, err
					}
					sc.Base64 = base64.StdEncoding.EncodeToString(b)
					sc.File = ""
				}
			}
		}
		if len(c.DataJson) > 0 {
			var m map[string]any
			if json.Unmarshal(c.DataJson, &m) == nil {
				sc.Data = m
			}
		}
		out = append(out, sc)
	}
	return out, nil
}
