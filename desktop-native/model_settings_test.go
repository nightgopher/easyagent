package main

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type unreadableModelSecrets struct {
	memorySecrets
	writes, deletes atomic.Int32
}

func (s *unreadableModelSecrets) Get(account string) (string, error) {
	value, err := s.memorySecrets.Get(account)
	if account == "model" && value == "" && err == nil {
		return "", fmt.Errorf("旧钥匙串授权无法读取，请重新填写模型 API 密钥")
	}
	return value, err
}
func (s *unreadableModelSecrets) Set(account, value string) error {
	s.writes.Add(1)
	return s.memorySecrets.Set(account, value)
}
func (s *unreadableModelSecrets) Delete(account string) error {
	s.deletes.Add(1)
	return s.memorySecrets.Delete(account)
}

func TestUnreadableModelKeyWithoutReplacementDoesNotClearCredential(t *testing.T) {
	a := alignmentPreview(t)
	store := &unreadableModelSecrets{}
	a.Live.Secrets = store
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: "http://127.0.0.1:1", Model: "old-model", HasKey: true}
	a.Settings.Mode, a.Settings.Provider = "在桌面客户端配置", "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL, a.Settings.Model = "http://127.0.0.1:1", "old-model"
	a.applyLiveModel()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-save"] })
	if !strings.Contains(a.Live.Error, "重新填写") || store.writes.Load() != 0 || store.deletes.Load() != 0 || !a.Live.Config.Model.HasKey {
		t.Fatal("unreadable credential was changed before a replacement was supplied", a.Live.Error)
	}
}

func TestReplacementModelKeyRepairsUnreadableLegacyCredential(t *testing.T) {
	a, provider, _, calls := nativeCoreFixture(t)
	store := &unreadableModelSecrets{}
	a.Live.Secrets = store
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: provider.URL, Model: "fixture-model", HasKey: true}
	a.Settings.Mode, a.Settings.Provider = "在桌面客户端配置", "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL, a.Settings.Model, a.Settings.APIKey = provider.URL, "fixture-repaired", "fixture-replacement"
	a.applyLiveModel()
	waitNative(t, a, func() bool {
		return !a.Live.Loading["model-save"] && a.Live.Config.Model.Model == "fixture-repaired" && a.Live.Connected
	})
	value, err := store.Get(modelSecretName(a.Live.Config.Model))
	if err != nil || value != "fixture-replacement" || calls.Load() != 0 || store.deletes.Load() != 0 {
		t.Fatal("replacement required access to the obsolete credential", err)
	}
}

func TestGlobalModelSettingsLayoutAndEditing(t *testing.T) {
	for _, width := range []int{900, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open = true
			a.Settings.Tab = "models"
			a.Live.Data["models"] = map[string]any{"current": map[string]any{"id": "global-default"}}
			a.current().Model = "session-override"
			tt := ui.NewTester(a.View, width, 820)
			for _, text := range []string{"全局模型配置", "主机默认模型", "global-default", "当前会话模型", "session-override"} {
				if !tt.HasText(text) {
					t.Fatal("missing context", text)
				}
			}
			click(t, tt, "配置全局模型")
			if a.Settings.Model != "global-default" {
				t.Fatal("default was copied from session instead of host")
			}
			if _, ok := tt.Find("全局模型服务地址"); !ok {
				t.Fatal("missing global connection form")
			}
			a.Live.Loading["model-save"] = true
			tt.Frame()
			click(t, tt, "全局默认模型")
			tt.Type("must-not-edit")
			if a.Settings.Model != "global-default" {
				t.Fatal("busy form still editable")
			}
			a.Live.Loading["model-save"] = false
			tt.Frame()
			a.Live.Data["model-check"] = map[string]any{"message": "old catalog", "data": []any{map[string]any{"id": "old-model"}}}
			click(t, tt, "全局模型服务地址")
			tt.Type("http://localhost:9999")
			tt.Frame()
			if a.Live.Data["model-check"] != nil {
				t.Fatalf("stale catalog survived URL edit: url=%q", a.Settings.BaseURL)
			}
		})
	}
}

func TestGlobalModelCatalogVirtualizesAndSearchesAfterScrolling(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Mode = "在桌面客户端配置"
	data := make([]any, 1200)
	for i := range data {
		data[i] = map[string]any{"id": fmt.Sprintf("model-%04d", i)}
	}
	a.Live.Data["model-check"] = map[string]any{"data": data}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Grow(1).Children(func() { a.liveModelSettings(c) })
	}, 900, 1400)
	if len(tt.Texts()) > 200 {
		t.Fatalf("built the entire default model catalog: %d texts", len(tt.Texts()))
	}
	viewport, ok := tt.Find("默认模型目录")
	if !ok || viewport.H == 0 {
		t.Fatal("missing model catalog")
	}
	tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 100000)
	for range 3 {
		tt.Frame()
	}
	if _, ok := tt.Find("设为默认模型：model-1199"); !ok {
		t.Fatal("last catalog model unreachable")
	}
	a.Settings.ModelCatalogSearch = "0050"
	tt.Frame()
	click(t, tt, "设为默认模型：model-0050")
	if a.Settings.Model != "model-0050" || len(tt.Texts()) > 200 {
		t.Fatal("filtered catalog selection failed")
	}
}

func TestModelCatalogCanBeDiscoveredBeforeChoosingModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != "GET" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"catalog-choice"}]}`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Settings.Mode = "在桌面客户端配置"
	a.Settings.Model = ""
	a.Settings.BaseURL = srv.URL
	a.Settings.APIKey = "fixture-key"
	a.checkLiveModel()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-check"] })
	if a.Live.Error != "" || len(list(object(a.Live.Data["model-check"])["data"])) != 1 {
		t.Fatal("discovery required a model", a.Live.Error)
	}
	if a.Settings.Model != "" {
		t.Fatal("discovery changed default without selection")
	}
}

func TestNewSessionUsesHostDefaultNotPreviousOverride(t *testing.T) {
	for _, scenario := range []string{"existing", "blank", "chosen"} {
		t.Run(scenario, func(t *testing.T) {
			request := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/sessions" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					request <- body
					fmt.Fprint(w, `{"id":"new-session"}`)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			a := alignmentPreview(t)
			a.Live.Client, _ = newBackendClient(srv.URL, "")
			a.current().Model = "previous-override"
			if scenario != "existing" {
				a.current().RemoteID = ""
			}
			if scenario == "chosen" {
				a.switchLiveModel("chosen-model")
			}
			a.createLiveSession("")
			body := <-request
			expected := ""
			if scenario == "chosen" {
				expected = "chosen-model"
			}
			if str(body["model"]) != expected {
				t.Fatal("incorrect model for new session", body["model"])
			}
			waitNative(t, a, func() bool { return !a.Live.Loading["create-session"] })
		})
	}
}

func TestGlobalModelApplyWithRealCore(t *testing.T) {
	a, provider, _, calls := nativeCoreFixture(t)
	a.createLiveSession("")
	waitNative(t, a, func() bool { return a.current().RemoteID != "" && a.current().Model != "" })
	id := a.current().RemoteID
	a.switchLiveModel("fixture-other")
	waitNative(t, a, func() bool { return a.current().Model == "fixture-other" })
	a.Settings.Mode = "在桌面客户端配置"
	a.Settings.Provider = "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL = provider.URL
	a.Settings.Model = "fixture-global"
	a.Settings.APIKey = "fixture-key"
	a.applyLiveModel()
	waitNative(t, a, func() bool {
		return a.Live.Config.Model.Model == "fixture-global" && a.Live.Connected && !a.Live.Connecting
	})
	a.refreshLiveSessions(id)
	waitNative(t, a, func() bool { return a.current().RemoteID == id && a.current().Model == "fixture-other" })
	a.createLiveSession("")
	waitNative(t, a, func() bool { return a.current().RemoteID != id && a.current().Model == "fixture-global" })
	if calls.Load() != 0 {
		t.Fatal("configuration triggered inference")
	}
}

func TestModelDiscoveryDropsResultsAfterLeavingSettings(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"data":[{"id":"stale"}]}`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Settings.Tab = "models"
	a.Settings.Mode = "在桌面客户端配置"
	a.Settings.BaseURL = srv.URL
	a.Settings.APIKey = "fixture-key"
	a.checkLiveModel()
	<-started
	a.closeSettings()
	close(release)
	waitNative(t, a, func() bool { return !a.Live.Loading["model-check"] })
	if a.Live.Data["model-check"] != nil {
		t.Fatal("late model discovery repopulated closed settings")
	}
}

func TestDefaultModelMenuScrollAndKeyboard(t *testing.T) {
	for _, width := range []int{900, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open, a.Settings.Tab = true, "models"
			a.Live.Models = []string{"coding"}
			for i := range 137 {
				a.Live.Models = append(a.Live.Models, fmt.Sprintf("catalog-model-%03d", i))
			}
			a.Live.Data["models"] = map[string]any{"current": map[string]any{"id": "coding"}}
			tt := ui.NewTester(a.View, width, 640)
			trigger, ok := tt.Find("选择主机默认模型")
			if !ok || trigger.W < 180 || trigger.H < 31.9 || trigger.H > 32.1 {
				t.Fatalf("default model trigger too small or mis-sized: %+v", trigger)
			}
			save, _ := tt.Find(SettingCopySaveDefaultModel)
			if d := (trigger.Y + trigger.H/2) - (save.Y + save.H/2); d < -1 || d > 1 {
				t.Fatalf("trigger and save not aligned: %+v %+v", trigger, save)
			}
			click(t, tt, "选择主机默认模型")
			viewport, ok := tt.Find("选择主机默认模型选项列表")
			if !ok || viewport.H > 261 || viewport.Y < 0 || viewport.Y+viewport.H > 640 {
				t.Fatalf("unbounded model menu: %+v", viewport)
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("default-model-menu-%d.png", width)))
			}
			tt.Move(viewport.X+viewport.W/2, viewport.Y+viewport.H/2)
			tt.Scroll(viewport.X+viewport.W/2, viewport.Y+viewport.H/2, 0, 100000)
			for range 5 {
				tt.Frame()
			}
			last, ok := tt.Find("catalog-model-136")
			if !ok || last.Y < viewport.Y || last.Y+last.H > viewport.Y+viewport.H+1 {
				t.Fatalf("wheel cannot reach last model: %+v in %+v", last, viewport)
			}
			tt.Key(0, ui.KeyHome)
			tt.Key(0, ui.KeyDown)
			tt.Frame()
			first, _ := tt.Find("catalog-model-000")
			if first.Y < viewport.Y || first.Y+first.H > viewport.Y+viewport.H+1 {
				t.Fatalf("keyboard did not reveal first model: %+v", first)
			}
			tt.Key(0, ui.KeyEnd)
			tt.Frame()
			tt.Key(0, ui.KeyEnter)
			if a.Settings.DefaultModelChoice != "catalog-model-136" || a.Live.Loading["model-default"] || str(object(object(a.Live.Data["models"])["current"])["id"]) != "coding" {
				t.Fatal("selection did not update the draft independently of saving")
			}
			if _, ok := tt.Find("选择主机默认模型选项列表"); ok {
				t.Fatal("selection did not close menu")
			}
		})
	}
}

// TestZhipuCodingCandidateMapsToOpenAIChannel：设置页选「智谱 GLM Coding Plan」
// 复用 openai provider 通道，/v4 端点地址原样保留（不被剥尾 /v1 影响）。
func TestZhipuCodingCandidateMapsToOpenAIChannel(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Mode, a.Settings.Provider = "在桌面客户端配置", SettingCopyZhipuProvider
	a.Settings.BaseURL, a.Settings.Model = SettingCopyZhipuBaseURL, "glm-5.3"
	cfg, _, err := a.modelCandidate()
	if err != nil {
		t.Fatal("zhipu candidate rejected:", err)
	}
	if cfg.Provider != "openai" {
		t.Fatal("zhipu provider label must map to the openai channel:", cfg.Provider)
	}
	if cfg.BaseURL != SettingCopyZhipuBaseURL {
		t.Fatal("coding endpoint URL must be preserved:", cfg.BaseURL)
	}
}

// TestExpectedModelFormZhipuBackfill：保存的智谱配置按地址特征回填下拉标签，
// 往返稳定；普通网关地址仍回填通用标签。
func TestExpectedModelFormZhipuBackfill(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: SettingCopyZhipuBaseURL, Model: "glm-5.3", HasKey: true}
	if f := a.expectedModelForm(); f.Provider != SettingCopyZhipuProvider || f.BaseURL != SettingCopyZhipuBaseURL {
		t.Fatal("zhipu saved config must backfill the zhipu label:", f.Provider, f.BaseURL)
	}
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: "http://localhost:4001", Model: "coding", HasKey: true}
	if f := a.expectedModelForm(); f.Provider != "OpenAI 兼容服务 / 网关" {
		t.Fatal("plain gateway config must keep the generic label:", f.Provider)
	}
}

func TestModelCredentialTargetDoesNotCrossEndpoints(t *testing.T) {
	old := modelConfig{Provider: "openai", BaseURL: SettingCopyZhipuBaseURL}
	for _, tc := range []struct {
		next modelConfig
		same bool
	}{
		{modelConfig{Provider: "openai", BaseURL: SettingCopyZhipuBaseURL + "/"}, true},
		{modelConfig{Provider: "openai", BaseURL: SettingCopyVolcBaseURL}, false},
		{modelConfig{Provider: "anthropic", BaseURL: SettingCopyZhipuBaseURL}, false},
	} {
		if got := sameModelCredentialTarget(old, tc.next); got != tc.same {
			t.Errorf("target %+v: got %v", tc.next, got)
		}
	}
}

func TestModelCatalogDoesNotSendStoredKeyToDifferentEndpoint(t *testing.T) {
	requests := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: "https://old.example", Model: "model", HasKey: true}
	a.Live.Secrets = &memorySecrets{values: map[string]string{"model": "old-secret"}}
	a.Settings.Mode, a.Settings.Provider = SettingCopyDesktopMode, "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL, a.Settings.Model, a.Settings.APIKey = srv.URL, "model", ""
	a.checkLiveModel()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-check"] })
	if a.Live.Error == "" {
		t.Fatal("expected endpoint change to require a key")
	}
	select {
	case <-requests:
		t.Fatal("sent request to another endpoint")
	default:
	}
}

func TestVolcCodingPlanConfigRoundTrip(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: SettingCopyVolcBaseURL, Model: "test", HasKey: true}
	if got := a.expectedModelForm().Provider; got != SettingCopyVolcProvider {
		t.Fatalf("provider: %s", got)
	}
}

func TestVersionedModelCatalogPath(t *testing.T) {
	for _, tc := range []struct{ base, path string }{{"https://example.com", "/v1/models"}, {"https://example.com/v1", "/models"}, {SettingCopyVolcBaseURL, "/models"}, {SettingCopyZhipuBaseURL, "/models"}} {
		if got := modelCatalogPath(tc.base); got != tc.path {
			t.Fatalf("%s: got %s", tc.base, got)
		}
	}
}

func TestOpenAIAPIConfigRoundTrip(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Config.Model = modelConfig{Mode: "override", Provider: "openai", BaseURL: SettingCopyOpenAIBaseURL, Model: "gpt-5", HasKey: true}
	if got := a.expectedModelForm().Provider; got != SettingCopyOpenAIProvider {
		t.Fatalf("provider: %s", got)
	}
}

func TestModelSourcesSeparateAccountAndAPIWithoutChangingActiveConfig(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "models"
	before := a.Live.Config.Model
	tt := ui.NewTester(a.View, 1240, 1000)
	for _, label := range []string{"ChatGPT 订阅登录", "OpenAI API Key", "智谱 GLM Coding Plan", "火山引擎 Coding Plan"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("missing source %q", label)
		}
	}
	click(t, tt, "ChatGPT 订阅登录")
	if !a.Settings.ModelShowChatGPT || a.Live.Config.Model != before {
		t.Fatal("opening account changed active model")
	}
	tt.Frame()
	if !tt.HasText("ChatGPT 登录 (Codex)") {
		t.Fatal("missing ChatGPT login")
	}
	click(t, tt, "OpenAI API Key")
	if a.Settings.ModelShowChatGPT || a.Settings.Provider != SettingCopyOpenAIProvider || a.Settings.BaseURL != SettingCopyOpenAIBaseURL || a.Live.Config.Model != before {
		t.Fatal("API selection changed active model or missed default endpoint")
	}
}

func TestModelSourceControlsFitSettingsPage(t *testing.T) {
	for _, width := range []int{640, 960, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open, a.Settings.Tab = true, "models"
			tt := ui.NewTester(a.View, width, 1100)
			for _, label := range []string{"ChatGPT 订阅登录", "OpenAI API Key", "智谱 GLM Coding Plan", "火山引擎 Coding Plan"} {
				box, ok := tt.Find(label)
				if !ok || box.X < 0 || box.X+box.W > float32(width) {
					t.Fatalf("%s outside window: %+v", label, box)
				}
			}
		})
	}
}

func TestSwitchingModelSourceKeepsUnsavedKeyUntilConfirmed(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "models"
	a.Settings.Mode, a.Settings.Provider = SettingCopyDesktopMode, SettingCopyOpenAIProvider
	a.Settings.BaseURL, a.Settings.APIKey = SettingCopyOpenAIBaseURL, "unsaved-key"
	a.selectModelSource("volc")
	if a.Settings.APIKey != "unsaved-key" || a.Settings.Provider != SettingCopyOpenAIProvider || a.Live.Confirm == nil {
		t.Fatal("source switch discarded draft")
	}
}

func TestSavedModelCredentialsRemainBoundToEndpoint(t *testing.T) {
	old := modelConfig{Mode: "override", Provider: "openai", BaseURL: "https://old.example", Model: "old", HasKey: true}
	other := modelConfig{Mode: "override", Provider: "openai", BaseURL: "https://new.example", Model: "new", HasKey: true, ProfileSecret: true}
	secrets := &memorySecrets{values: map[string]string{"model": "legacy-key", modelSecretName(other): "other-key"}}
	profiles := map[string]modelConfig{modelProfileID(old): old, modelProfileID(other): other}
	for _, tc := range []struct {
		cfg  modelConfig
		want string
	}{{old, "legacy-key"}, {other, "other-key"}} {
		got, err := savedModelCredential(tc.cfg, other, profiles, secrets)
		if err != nil || got != tc.want {
			t.Fatalf("credential for %s: %q %v", tc.cfg.BaseURL, got, err)
		}
	}
	unknown := modelConfig{Mode: "override", Provider: "openai", BaseURL: "https://third.example"}
	if _, err := savedModelCredential(unknown, other, profiles, secrets); err == nil {
		t.Fatal("unknown endpoint reused another credential")
	}
	copy := cloneConfig(clientConfig{ModelProfiles: profiles})
	delete(copy.ModelProfiles, modelProfileID(old))
	if len(profiles) != 2 {
		t.Fatal("config snapshot shares profile map")
	}
}

func TestLegacyPreviousModelVisibleWhileUsingEasyCode(t *testing.T) {
	legacy := modelConfig{Mode: "override", Provider: "openai", BaseURL: SettingCopyOpenAIBaseURL, Model: "gpt", HasKey: true}
	cfg := clientConfig{Model: modelConfig{Mode: "easycode"}, PreviousModel: &legacy}
	p, ok := availableModelProfiles(cfg)[modelProfileID(legacy)]
	if !ok || p.Model != "gpt" || p.ProfileSecret {
		t.Fatal("legacy profile not visible")
	}
}

func TestModelProfilesSwitchAcrossRestarts(t *testing.T) {
	a, first, _, _ := nativeCoreFixture(t)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"other"}]}`) }))
	defer second.Close()
	apply := func(endpoint, model, key string) {
		t.Helper()
		a.Settings.Mode, a.Settings.Provider = SettingCopyDesktopMode, "OpenAI 兼容服务 / 网关"
		a.Settings.BaseURL, a.Settings.Model, a.Settings.APIKey = endpoint, model, key
		a.applyLiveModel()
		waitNative(t, a, func() bool {
			return !a.Live.Loading["model-save"] && (a.Live.Error != "" || a.Live.Config.Model.Model == model && a.Live.Connected)
		})
		if a.Live.Error != "" {
			t.Fatal(a.Live.Error)
		}
	}
	apply(first.URL, "first-model", "first-key")
	apply(second.URL, "second-model", "second-key")
	apply(first.URL, "first-model", "")
	if len(a.Live.Config.ModelProfiles) != 2 {
		t.Fatalf("profiles: %d", len(a.Live.Config.ModelProfiles))
	}
	if a.Live.Config.Model.Mode != "profile" || !a.Live.Config.Model.ProfileSecret {
		t.Fatal("profile must use its own stored credential mode")
	}
	data, err := os.ReadFile(filepath.Join(a.Live.Dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted clientConfig
	if err := json.Unmarshal(data, &persisted); err != nil || len(persisted.ModelProfiles) != 2 {
		t.Fatalf("persisted profiles: %v", err)
	}
	if strings.Contains(string(data), "first-key") || strings.Contains(string(data), "second-key") {
		t.Fatal("credential written to connections.json")
	}
	for _, tc := range []struct{ url, key string }{{first.URL, "first-key"}, {second.URL, "second-key"}} {
		cfg := modelConfig{Provider: "openai", BaseURL: tc.url, ProfileSecret: true}
		got, err := a.Live.Secrets.Get(modelSecretName(cfg))
		if err != nil || got != tc.key {
			t.Fatalf("wrong stored credential for %s: %v", tc.url, err)
		}
	}
}

func TestLegacyModelProfilePreservedWhenSwitching(t *testing.T) {
	a, first, _, _ := nativeCoreFixture(t)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"other"}]}`) }))
	defer second.Close()
	legacy := modelConfig{Mode: "override", Provider: "openai", BaseURL: first.URL, Model: "legacy-model", HasKey: true}
	a.Live.Config.Model = legacy
	if err := a.Live.Secrets.Set("model", "legacy-key"); err != nil {
		t.Fatal(err)
	}
	a.Settings.Mode, a.Settings.Provider = SettingCopyDesktopMode, "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL, a.Settings.Model, a.Settings.APIKey = second.URL, "other-model", "other-key"
	a.applyLiveModel()
	waitNative(t, a, func() bool {
		return !a.Live.Loading["model-save"] && (a.Live.Error != "" || a.Live.Config.Model.Model == "other-model" && a.Live.Connected)
	})
	if a.Live.Error != "" {
		t.Fatal(a.Live.Error)
	}
	p, ok := a.Live.Config.ModelProfiles[modelProfileID(legacy)]
	if !ok || p.ProfileSecret || p.BaseURL != legacy.BaseURL {
		t.Fatal("legacy profile not retained")
	}
	key, err := savedModelCredential(legacy, a.Live.Config.Model, a.Live.Config.ModelProfiles, a.Live.Secrets)
	if err != nil || key != "legacy-key" {
		t.Fatalf("legacy key unavailable: %v", err)
	}
}

func TestModelProfileSaveFailureKeepsOldConfiguration(t *testing.T) {
	a, provider, _, _ := nativeCoreFixture(t)
	path := filepath.Join(a.Live.Dir, "connections.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := a.Live.Config.Model
	a.Settings.Mode, a.Settings.Provider = SettingCopyDesktopMode, "OpenAI 兼容服务 / 网关"
	a.Settings.BaseURL, a.Settings.Model, a.Settings.APIKey = provider.URL, "failure-model", "test-key"
	target := modelConfig{Provider: "openai", BaseURL: provider.URL, ProfileSecret: true}
	if err := a.Live.Secrets.Set(modelSecretName(target), "previous-orphan"); err != nil {
		t.Fatal(err)
	}
	a.applyLiveModel()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-save"] && a.Live.Error != "" })
	if a.Live.Config.Model != old {
		t.Fatal("failed save changed active model")
	}
	key, err := a.Live.Secrets.Get(modelSecretName(target))
	if err != nil || key != "previous-orphan" {
		t.Fatal("failed save did not restore prior credential", err)
	}
}
