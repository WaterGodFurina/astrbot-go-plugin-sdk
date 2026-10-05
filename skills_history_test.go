package sdk

import (
	"context"
	"testing"
)

// TestSkillsHistoryRoundTrip 验证 skills + platform-message-history RPC 的
// 往返：宿主侧 SetHostHooks mock 提供数据 → native HostService 处理请求 →
// 强类型解码（SkillInfo / PMHistoryRecord 字段与宿主/Python SDK 对齐）。
//
// 不直接调用 Host.* 客户端方法（它们需要 go-plugin broker 的联网通道）；
// 这里验证 server 端 + 解码路径，Host.* 的 broker 握手在 CI 的插件级测试覆盖。
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

	srv := &HostServiceServer{pluginID: "test_plugin_skills"}

	// ListSkills → 强类型解码
	skills := srv.ListSkills(context.Background())
	if len(skills) != 1 {
		t.Fatalf("ListSkills: want 1 skill, got %d", len(skills))
	}
	var s SkillInfo
	s.FromMap(skills[0])
	if s.Name != "weather" || !s.Active || s.Description != "查询天气" {
		t.Fatalf("SkillInfo decode mismatch: %+v", s)
	}
	if s.SourceType != SkillSourceLocalOnly {
		t.Fatalf("SkillInfo.SourceType: want local_only, got %q", s.SourceType)
	}

	// SetSkillActive / DeleteSkill（hooks mock 断言内部调用成功）
	if err := srv.SetSkillActive(context.Background(), "weather", true); err != nil {
		t.Fatalf("SetSkillActive: %v", err)
	}
	if err := srv.DeleteSkill(context.Background(), "obsolete"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}

	// GetPlatformMessageHistory → 强类型解码
	records := srv.GetPlatformMessageHistory(context.Background(), "aiocqhttp", "g:1", 50)
	if len(records) != 1 {
		t.Fatalf("GetPMHistory: want 1 record, got %d", len(records))
	}
	var r PMHistoryRecord
	r.FromMap(records[0])
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

	// Insert → 强类型解码（native content 直接传 any，无需 JSON 编解码）
	ins := srv.InsertPlatformMessageHistory(context.Background(), "aiocqhttp", "g:1", "u2",
		map[string]any{"type": "user", "message": []any{"hello"}}, "ck-1", 200)
	if ins == nil {
		t.Fatalf("InsertPMHistory: nil record")
	}
	var ir PMHistoryRecord
	ir.FromMap(ins)
	if ir.ID != 99 || ir.PlatformID != "aiocqhttp" {
		t.Fatalf("Insert PMHistory mismatch: %+v", ir)
	}

	// Update / Delete
	if err := srv.UpdatePlatformMessageHistory(context.Background(), 7, map[string]any{"x": 1}, "ck-2"); err != nil {
		t.Fatalf("UpdatePMHistory: %v", err)
	}
	if err := srv.DeletePlatformMessageHistory(context.Background(), 7); err != nil {
		t.Fatalf("DeletePMHistory: %v", err)
	}
}

// TestSkillInfoFromMapFallback 验证 SkillInfo.FromMap 对缺失/异常字段安全取值。
func TestSkillInfoFromMapFallback(t *testing.T) {
	var s SkillInfo
	s.FromMap(map[string]any{
		"name":         "a",
		"active":       true,      // bool
		"readonly":     "notbool", // 非 bool → false
		"source_type":  nil,       // 缺失 → local_only
		"source_label": "custom",  // 非空自定义 → 保留
	})
	if s.Name != "a" || !s.Active || s.Readonly {
		t.Fatalf("SkillInfo.FromMap fallback mismatch: %+v", s)
	}
	if s.SourceType != SkillSourceLocalOnly || s.SourceLabel != "custom" {
		t.Fatalf("SkillInfo.FromMap defaults mismatch: %+v", s)
	}
}

// TestPMHistoryFromMapInt64 验证 PMHistoryRecord.FromMap 的 id 转换
// （宿主 JSON 里数字为 float64，int64 也要兼容）。
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
