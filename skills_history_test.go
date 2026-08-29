package sdk

import (
	"context"
	"encoding/json"
	"testing"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// TestSkillsHistoryRoundTrip 验证 skills + platform-message-history RPC 的
// 往返：宿主侧 SetHostHooks mock 提供数据 → hostServiceServer 处理 proto
// 请求 → 强类型解码（SkillInfo / PMHistoryRecord 字段与宿主/Python SDK 对齐）。
//
// 不直接调用 Host.* 客户端方法（它们需要 go-plugin broker 的联网通道）；
// 这里验证 server RPC + 解码路径，Host.* 的 broker 握手在 CI 的插件级测试覆盖。
func TestSkillsHistoryRoundTrip(t *testing.T) {
	SetHostHooks(HostServiceHooks{
		ListSkills: func() []map[string]any {
			return []map[string]any{
				{
					"name":           "weather",
					"description":    "查询天气",
					"path":           "data/skills/weather/SKILL.md",
					"active":         true,
					"source_type":    "local_only",
					"source_label":   "local",
					"local_exists":   true,
					"sandbox_exists": false,
					"plugin_name":    "",
					"readonly":       false,
					"preset":         false,
				},
			}
		},
		SetSkillActive: func(name string, active bool) error {
			if name != "weather" {
				t.Fatalf("SetSkillActive name: want weather, got %s", name)
			}
			if !active {
				t.Fatalf("SetSkillActive active: want true, got false")
			}
			return nil
		},
		DeleteSkill: func(name string) error {
			if name != "obsolete" {
				t.Fatalf("DeleteSkill name: want obsolete, got %s", name)
			}
			return nil
		},
		GetPlatformMessageHistory: func(platformID, userID string, limit int32) []map[string]any {
			if platformID != "aiocqhttp" || userID != "g:1" {
				t.Fatalf("GetPMHistory ids: got %s / %s", platformID, userID)
			}
			return []map[string]any{
				{
					"id":          7,
					"platform_id": "aiocqhttp",
					"user_id":     "g:1",
					"sender_id":   "u1",
					"content":     map[string]any{"type": "user", "message": []any{"hi"}},
					"created_at":  "2026-01-01T00:00:00Z",
				},
			}
		},
		InsertPlatformMessageHistory: func(platformID, userID, senderID string, content any, llmCheckpointID string, maxMessages int32) map[string]any {
			return map[string]any{
				"id":          99,
				"platform_id": platformID,
				"user_id":     userID,
				"sender_id":   senderID,
				"content":     content,
				"created_at":  "2026-01-02T00:00:00Z",
			}
		},
		UpdatePlatformMessageHistory: func(id int64, content any, llmCheckpointID string) error {
			if id != 7 {
				t.Fatalf("UpdatePMHistory id: want 7, got %d", id)
			}
			return nil
		},
		DeletePlatformMessageHistory: func(id int64) error {
			if id != 7 {
				t.Fatalf("DeletePMHistory id: want 7, got %d", id)
			}
			return nil
		},
	})

	srv := &hostServiceServer{pluginID: "test_plugin_skills"}

	// ListSkills RPC → 强类型解码
	resp, err := srv.ListSkills(context.Background(), &sdkv1.Empty{})
	if err != nil {
		t.Fatalf("ListSkills RPC: %v", err)
	}
	if len(resp.SkillsJson) != 1 {
		t.Fatalf("ListSkills: want 1 skill, got %d", len(resp.SkillsJson))
	}
	var m map[string]any
	if err := json.Unmarshal(resp.SkillsJson[0], &m); err != nil {
		t.Fatalf("skills_json unmarshal: %v", err)
	}
	var s SkillInfo
	s.FromMap(m)
	if s.Name != "weather" || !s.Active || s.Description != "查询天气" {
		t.Fatalf("SkillInfo decode mismatch: %+v", s)
	}
	if s.SourceType != SkillSourceLocalOnly {
		t.Fatalf("SkillInfo.SourceType: want local_only, got %q", s.SourceType)
	}

	// SetSkillActive / DeleteSkill RPC（hooks mock 断言内部调用成功）
	if _, err := srv.SetSkillActive(context.Background(), &sdkv1.SetSkillActiveRequest{Name: "weather", Active: true}); err != nil {
		t.Fatalf("SetSkillActive RPC: %v", err)
	}
	if _, err := srv.DeleteSkill(context.Background(), &sdkv1.DeleteSkillRequest{Name: "obsolete"}); err != nil {
		t.Fatalf("DeleteSkill RPC: %v", err)
	}

	// GetPlatformMessageHistory RPC → 强类型解码
	gmh, err := srv.GetPlatformMessageHistory(context.Background(), &sdkv1.GetPMHistoryRequest{
		PlatformId: "aiocqhttp",
		UserId:     "g:1",
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("GetPMHistory RPC: %v", err)
	}
	if len(gmh.RecordsJson) != 1 {
		t.Fatalf("GetPMHistory: want 1 record, got %d", len(gmh.RecordsJson))
	}
	var rm map[string]any
	if err := json.Unmarshal(gmh.RecordsJson[0], &rm); err != nil {
		t.Fatalf("records_json unmarshal: %v", err)
	}
	var r PMHistoryRecord
	r.FromMap(rm)
	if r.ID != 7 || r.SenderID != "u1" {
		t.Fatalf("PMHistory decode mismatch: %+v", r)
	}
	content, ok := r.Content.(map[string]any)
	if !ok {
		t.Fatalf("PMHistory.Content: not a map, got %T", r.Content)
	}
	if content["type"] != "user" {
		t.Fatalf("PMHistory.Content type: want user, got %v", content["type"])
	}

	// Insert RPC → 强类型解码
	insJSON, err := json.Marshal(map[string]any{"type": "user", "message": []any{"hello"}})
	if err != nil {
		t.Fatalf("marshal insert content: %v", err)
	}
	ins, err := srv.InsertPlatformMessageHistory(context.Background(), &sdkv1.InsertPMHistoryRequest{
		PlatformId:       "aiocqhttp",
		UserId:           "g:1",
		SenderId:         "u2",
		ContentJson:      insJSON,
		LlmCheckpointId:  "ck-1",
		MaxMessages:      200,
	})
	if err != nil {
		t.Fatalf("InsertPMHistory RPC: %v", err)
	}
	if len(ins.RecordJson) == 0 {
		t.Fatalf("InsertPMHistory: empty record_json")
	}
	var rim map[string]any
	if err := json.Unmarshal(ins.RecordJson, &rim); err != nil {
		t.Fatalf("insert record unmarshal: %v", err)
	}
	var ir PMHistoryRecord
	ir.FromMap(rim)
	if ir.ID != 99 || ir.PlatformID != "aiocqhttp" {
		t.Fatalf("Insert PMHistory mismatch: %+v", ir)
	}

	// Update / Delete RPC
	upJSON, _ := json.Marshal(map[string]any{"x": 1})
	if _, err := srv.UpdatePlatformMessageHistory(context.Background(), &sdkv1.UpdatePMHistoryRequest{
		Id:              7,
		ContentJson:     upJSON,
		LlmCheckpointId: "ck-2",
	}); err != nil {
		t.Fatalf("UpdatePMHistory RPC: %v", err)
	}
	if _, err := srv.DeletePlatformMessageHistory(context.Background(), &sdkv1.DeletePMHistoryRequest{Id: 7}); err != nil {
		t.Fatalf("DeletePMHistory RPC: %v", err)
	}
}

// TestSkillInfoFromMapFallback 验证 SkillInfo.FromMap 对缺失/异常字段安全取值。
func TestSkillInfoFromMapFallback(t *testing.T) {
	var s SkillInfo
	s.FromMap(map[string]any{
		"name":        "a",
		"active":      true,      // bool
		"readonly":    "notbool", // 非 bool → false
		"source_type": nil,       // 缺失 → local_only
		"source_label": "custom", // 非空自定义 → 保留
	})
	if s.Name != "a" || !s.Active || s.Readonly {
		t.Fatalf("SkillInfo.FromMap fallback mismatch: %+v", s)
	}
	if s.SourceType != SkillSourceLocalOnly || s.SourceLabel != "custom" {
		t.Fatalf("SkillInfo.FromMap defaults mismatch: %+v", s)
	}
}

// TestPMHistoryFromMapInt64 验证 PMHistoryRecord.FromMap 的 id 转换
//（宿主 JSON 里数字为 float64，int64 也要兼容）。
func TestPMHistoryFromMapInt64(t *testing.T) {
	var r PMHistoryRecord
	r.FromMap(map[string]any{
		"id":          42,
		"platform_id": "telegram",
		"user_id":     "u1",
		"content":     "text",
	})
	if r.ID != 42 || r.PlatformID != "telegram" || r.Content != "text" {
		t.Fatalf("PMHistoryRecord.FromMap mismatch: %+v", r)
	}
}