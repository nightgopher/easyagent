package agentdata

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedStewardV1 writes a pre-v2 steward record: no preset_version, customized
// role text, revoked tools and user toggles — what a v0.48 host has on disk.
func seedStewardV1(t *testing.T, dir string) {
	t.Helper()
	v1 := Agent{
		ID: StewardID, Name: "个人管家", MemoryEnabled: false, Disabled: true,
		Tools:  []string{"host_work_search", "local_time"}, // 其余工具已被用户撤销
		Skills: []string{"翻译"}, Knowledge: []KnowledgeBinding{}, Version: 3,
		UpdatedAt: "2026-11-01T00:00:00Z", Instructions: "v1 旧定位：你不是必经入口，不代替专属 Agent 委派任务。",
	}
	data, err := json.Marshal(v1)
	require.NoError(t, err)
	db, err := New(dir).open(context.Background())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(context.Background(),
		"INSERT INTO business_agents(id,definition,version) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING",
		StewardID, string(data), v1.Version)
	require.NoError(t, err)
}

func TestStewardPresetUpgradesV1InPlace(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	seedStewardV1(t, dir)
	store := New(dir)
	upgraded, err := store.EnsureSteward(ctx)
	require.NoError(t, err)

	// 角色定义整体升级到当前预设。
	require.Equal(t, int64(stewardPresetVersion), upgraded.PresetVersion)
	require.Equal(t, stewardPreset().Instructions, upgraded.Instructions)
	require.Equal(t, "个人管家", upgraded.Name)
	// 新预设工具被加入，用户保留/撤销过的工具不动。
	require.Contains(t, upgraded.Tools, "host_session_create")
	require.Contains(t, upgraded.Tools, "local_time")
	require.NotContains(t, upgraded.Tools, "host_session_search")
	// 用户开关与自定义清单不被重置。
	require.True(t, upgraded.Disabled)
	require.False(t, upgraded.MemoryEnabled)
	require.Equal(t, []string{"翻译"}, upgraded.Skills)
	// 记录修订号在原基础上自增一次。
	require.Equal(t, int64(4), upgraded.Version)

	// 升级幂等：再开不重置、不追加版本。
	again, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, upgraded, again)
}

func TestStewardPresetStableAfterUpgrade(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := New(dir)
	first, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, StewardID, first.ID)
	require.NotContains(t, first.Tools, "write")
	require.NotContains(t, first.Tools, "task")
	first.Instructions, first.Tools, first.Disabled, first.MemoryEnabled = "我的管家说明", []string{"local_time"}, true, false
	first, err = store.SaveAgent(ctx, first)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		wg.Go(func() { _, err := New(dir).EnsureSteward(ctx); errors <- err })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	reopened, err := New(dir).EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, first, reopened)
	all, err := store.ListAgents(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestStewardV2UpgradePreservesCustomizationsAndRevocations(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())
	first, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	first.PresetVersion = 2
	first.Instructions = "用户自定义管家职责"
	first.Name = "我的管家"
	first.Tools = []string{"host_work_search", "local_time"}
	data, err := json.Marshal(first)
	require.NoError(t, err)
	db, err := store.open(ctx)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE business_agents SET definition=? WHERE id=?", string(data), first.ID)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	upgraded, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, "我的管家", upgraded.Name)
	require.Contains(t, upgraded.Instructions, "用户自定义管家职责")
	require.Contains(t, upgraded.Instructions, "host_work_close")
	require.Contains(t, upgraded.Tools, "host_work_close")
	require.NotContains(t, upgraded.Tools, "host_session_create")
	require.NotContains(t, upgraded.Tools, "host_session_search")
}

func TestStewardPresetSurvivesClientSaveWithoutPresetVersion(t *testing.T) {
	// 桌面/移动端的定义结构不携带 preset_version；GET→改→PUT 回写不得把预设版本
	// 抹成 0，否则下次 EnsureSteward 会再触发一次升级，覆盖用户自定义的角色文本。
	ctx, dir := context.Background(), t.TempDir()
	store := New(dir)
	base, err := store.EnsureSteward(ctx)
	require.NoError(t, err)

	saved := base
	saved.PresetVersion = 0 // 模拟不含 preset_version 字段的客户端回写
	saved.Instructions = "用户自定义说明"
	saved.Disabled = true
	saved, err = store.SaveAgent(ctx, saved)
	require.NoError(t, err)
	require.Equal(t, int64(stewardPresetVersion), saved.PresetVersion)
	require.Equal(t, "用户自定义说明", saved.Instructions)

	reopened, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, reopened)
	require.True(t, reopened.Disabled)
	require.Equal(t, "用户自定义说明", reopened.Instructions)
}

func TestStewardPresetMentionsSessionLifecycleRules(t *testing.T) {
	// 角色文案与单会话产品语义互相锁定，防止无意回退。
	text := stewardPreset().Instructions
	require.True(t, strings.Contains(text, "唯一"))
	require.True(t, strings.Contains(text, "/clear"))
	require.True(t, strings.Contains(text, "host_session_create"))
	require.True(t, slices.Contains(stewardPreset().Tools, "host_session_create"))
}
