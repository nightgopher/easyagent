package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mediaGateway serves the resource API locally and records what was asked
// of it. The client carries a cached token, so no auth call is made.
type mediaGateway struct {
	*Gateway
	status int
	paths  []string
}

func newMediaGateway(t *testing.T, body []byte) *mediaGateway {
	t.Helper()
	m := &mediaGateway{status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.paths = append(m.paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(m.status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	m.Gateway = &Gateway{
		client:     &Client{cachedToken: "token", tokenExpiresAt: time.Now().Add(time.Hour)},
		openAPI:    server.URL,
		httpClient: server.Client(),
	}
	return m
}

func message(content string) *larkim.EventMessage {
	id := "omsg_1"
	return &larkim.EventMessage{Content: &content, MessageId: &id}
}

func TestImagesAreSavedInTheChatInbox(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("\x89PNG bytes"))
	g.SetInbox(func(chatKey string) string {
		if chatKey == "oc_site" {
			return filepath.Join(workspace, ".agents", "tmp", "inbox")
		}
		return ""
	})

	text := g.handleImageMessage(context.Background(), message(`{"image_key":"img_v3_abc"}`), "oc_site")

	saved := filepath.Join(workspace, ".agents", "tmp", "inbox", "feishu-image-img_v3_abc.png")
	assert.Equal(t, "![image]("+saved+")", text)
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG bytes"), data)
	assert.Equal(t, []string{"/open-apis/im/v1/messages/omsg_1/resources/img_v3_abc?type=image"}, g.paths)
}

func TestImagesWithoutAnInboxKeepTheTempDir(t *testing.T) {
	g := newMediaGateway(t, []byte("x"))

	text := g.handleImageMessage(context.Background(), message(`{"image_key":"img_key"}`), "oc_site")

	saved := filepath.Join(os.TempDir(), "feishu-image-img_key.png")
	assert.Equal(t, "![image]("+saved+")", text)
	t.Cleanup(func() { _ = os.Remove(saved) })
}

func TestFilesAreSavedOnlyWithAnInbox(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("video bytes"))

	// Outside the workspace the model could not read the file, so it is skipped.
	assert.Empty(t, g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_x","file_name":"hero.mp4"}`), "oc_site", "file"))
	assert.Empty(t, g.paths)

	inbox := filepath.Join(workspace, ".agents", "tmp", "inbox")
	g.SetInbox(func(string) string { return inbox })
	// A user-supplied name never leaves the inbox.
	text := g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_x","file_name":"../../etc/头图.mp4"}`), "oc_site", "file")

	saved := filepath.Join(inbox, "头图.mp4")
	assert.Equal(t, "[文件: ../../etc/头图.mp4] 已保存到 "+saved, text)
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, []byte("video bytes"), data)
	assert.Equal(t, "/open-apis/im/v1/messages/omsg_1/resources/file_v3_x?type=file", g.paths[len(g.paths)-1])
	assert.NoDirExists(t, filepath.Join(workspace, "etc"))
}

func TestVideoMessagesAreSavedAsFiles(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("video bytes"))
	g.SetInbox(func(string) string { return filepath.Join(workspace, ".agents/tmp/inbox") })

	text := g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_v","image_key":"img_v3_i"}`), "oc_site", "media")

	assert.Equal(t, "[文件: feishu-video-file_v3_v.mp4] 已保存到 "+filepath.Join(workspace, ".agents/tmp/inbox/feishu-video-file_v3_v.mp4"), text)
}

func TestUnreadableMediaIsNamedInTheMessage(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("denied"))
	g.SetInbox(func(string) string { return filepath.Join(workspace, ".inbox") })

	g.status = http.StatusForbidden
	assert.Equal(t, "[图片消息: img_key]", g.handleImageMessage(context.Background(), message(`{"image_key":"img_key"}`), "oc_site"))
	assert.Contains(t, g.handleFileMessage(context.Background(), message(`{"file_key":"file_key","file_name":"a.pdf"}`), "oc_site", "file"), "下载失败")
	assert.NoDirExists(t, filepath.Join(workspace, ".inbox"))
}

func TestInboxSubdirRejectsUnsafeValues(t *testing.T) {
	for value, want := range map[string]string{
		"":                    "",
		"   ":                 "",
		".agents/tmp/inbox":   ".agents/tmp/inbox",
		"/.agents/tmp/inbox/": ".agents/tmp/inbox",
		"../elsewhere":        "",
		"a/../../b":           "",
		"~/inbox":             "",
		"/etc/passwd":         "etc/passwd",
	} {
		assert.Equal(t, want, inboxSubdir(value), "%q", value)
	}
}

func TestHandlerInboxDirFollowsTheChatWorkspace(t *testing.T) {
	workspace, project := t.TempDir(), t.TempDir()
	handler := &Handler{workspace: workspace, inbox: ".agents/tmp/inbox"}
	assert.Equal(t, filepath.Join(workspace, ".agents", "tmp", "inbox"), handler.InboxDir("oc_unbound"))

	handler.routes = map[string]*ChatRoute{"oc_site": {ProjectRoot: project}}
	assert.Equal(t, filepath.Join(project, ".agents", "tmp", "inbox"), handler.InboxDir("oc_site"))

	// A bridge without an inbox, or without a workspace for the chat, has none.
	assert.Empty(t, (&Handler{workspace: workspace}).InboxDir("oc_site"))
	assert.Empty(t, (&Handler{inbox: "inbox"}).InboxDir("oc_site"))
}

func TestInboxSubdirIsReadFromTheEnvironment(t *testing.T) {
	// The home directory is redirected so the handler never touches the
	// developer's own routes file.
	t.Setenv("EA_HOME", t.TempDir())
	t.Setenv("FEISHU_INBOX_DIR", ".agents/tmp/inbox")
	assert.Equal(t, ".agents/tmp/inbox", NewHandler("http://127.0.0.1:8080", "cli_x", nil, "/tmp/site").inbox)

	t.Setenv("FEISHU_INBOX_DIR", "../elsewhere")
	assert.Empty(t, NewHandler("http://127.0.0.1:8080", "cli_x", nil, "/tmp/site").inbox)
}
