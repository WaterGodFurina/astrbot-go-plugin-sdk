// proto.go — SDKEvent ⇄ Event / proto Component ⇄ Component 转换（P1 native data plane）。
//
// 新协议下 Event / Message Chain 走 protobuf 原生（0 次 JSON）；仅动态
// metadata / Component.Data（Json 卡片）保留 JSON。二进制走 bytes / Blob。
package sdk

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// EventToSDKEvent 把 SDK Event（struct）转成 proto SDKEvent。固定字段直拷，
// Chain 走 repeated Component，仅 Metadata 做一次 JSON（动态结构）。
func EventToSDKEvent(e *Event) *sdkv1.SDKEvent {
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

// SDKEventToEvent 把 proto SDKEvent 还原为 SDK Event（固定字段直拷，metadata
// 一次 JSON 解析，Components 走 proto→struct）。
func SDKEventToEvent(se *sdkv1.SDKEvent) *Event {
	if se == nil {
		return nil
	}
	e := &Event{
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

// componentsToProto 把 SDK Component 切片转成 proto Component。
// 媒体 Base64 转 bytes base64_data；Json 卡片 Data 保留 data_json。
func componentsToProto(chain []Component) []*sdkv1.Component {
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
		out = append(out, pc)
	}
	return out
}

// protoToComponents 把 proto Component 还原为 SDK Component。
// base64_data → Base64 string；data_json → Data map。
func protoToComponents(comps []*sdkv1.Component) []Component {
	out := make([]Component, 0, len(comps))
	for _, c := range comps {
		if c == nil {
			continue
		}
		sc := Component{
			Type:     ComponentType(c.Type),
			Text:     c.Text,
			TargetID: c.TargetId,
			Name:     c.Name,
			URL:      c.Url,
			Path:     c.Path,
			File:     c.File,
			FileID:   c.FileId,
			ID:       c.Id,
		}
		if len(c.Base64Data) > 0 {
			sc.Base64 = base64.StdEncoding.EncodeToString(c.Base64Data)
		}
		if len(c.DataJson) > 0 {
			var m map[string]any
			if json.Unmarshal(c.DataJson, &m) == nil {
				sc.Data = m
			}
		}
		out = append(out, sc)
	}
	return out
}

var _ = fmt.Sprintf
