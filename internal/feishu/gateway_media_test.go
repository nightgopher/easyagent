package feishu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	handler := &Handler{inbox: ".agents/tmp/inbox", routes: map[string]*ChatRoute{"oc_site": {ProjectRoot: workspace}}}
	g.SetInbox(handler.InboxDir)

	text := g.handleImageMessage(context.Background(), message(`{"image_key":"img_v3_abc"}`), "oc_site")

	saved := strings.TrimSuffix(strings.TrimPrefix(text, "![image]("), ")")
	assert.Equal(t, handler.InboxDir("oc_site"), filepath.Dir(saved))
	assert.Regexp(t, `^feishu-image-img_v3_abc-[0-9]+\.png$`, filepath.Base(saved))
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG bytes"), data)
	assert.Equal(t, []string{"/open-apis/im/v1/messages/omsg_1/resources/img_v3_abc?type=image"}, g.paths)
}

func TestImagesWithoutAnInboxKeepTheTempDir(t *testing.T) {
	g := newMediaGateway(t, []byte("x"))

	text := g.handleImageMessage(context.Background(), message(`{"image_key":"img_key"}`), "oc_site")

	saved := strings.TrimSuffix(strings.TrimPrefix(text, "![image]("), ")")
	temp, err := filepath.EvalSymlinks(os.TempDir())
	require.NoError(t, err)
	assert.Equal(t, temp, filepath.Dir(saved))
	assert.Regexp(t, `^feishu-image-img_key-[0-9]+\.png$`, filepath.Base(saved))
	t.Cleanup(func() { _ = os.Remove(saved) })
}

func TestFilesAreSavedOnlyWithAnInbox(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("video bytes"))

	// Outside the workspace the model could not read the file, so it is skipped.
	assert.Empty(t, g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_x","file_name":"hero.mp4"}`), "oc_site", "file"))
	assert.Empty(t, g.paths)

	inbox := filepath.Join(workspace, ".agents", "tmp", "inbox")
	g.SetInbox((&Handler{workspace: workspace, inbox: ".agents/tmp/inbox"}).InboxDir)
	// A user-supplied name never leaves the inbox.
	text := g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_x","file_name":"../../etc/头图.mp4"}`), "oc_site", "file")

	saved := strings.TrimPrefix(text, "[文件: 头图.mp4] 已保存到 ")
	canonical, err := filepath.EvalSymlinks(inbox)
	require.NoError(t, err)
	assert.Equal(t, canonical, filepath.Dir(saved))
	assert.Regexp(t, `^头图-[0-9]+\.mp4$`, filepath.Base(saved))
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, []byte("video bytes"), data)
	assert.Equal(t, "/open-apis/im/v1/messages/omsg_1/resources/file_v3_x?type=file", g.paths[len(g.paths)-1])
	assert.NoDirExists(t, filepath.Join(workspace, "etc"))
}

func TestVideoMessagesAreSavedAsFiles(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("video bytes"))
	g.SetInbox((&Handler{workspace: workspace, inbox: ".agents/tmp/inbox"}).InboxDir)

	text := g.handleFileMessage(context.Background(), message(`{"file_key":"file_v3_v","image_key":"img_v3_i"}`), "oc_site", "media")

	assert.Contains(t, text, "[文件: feishu-video-file_v3_v.mp4] 已保存到 ")
	assert.Regexp(t, `feishu-video-file_v3_v-[0-9]+\.mp4$`, text)
}

func TestUnreadableMediaIsNamedInTheMessage(t *testing.T) {
	workspace := t.TempDir()
	g := newMediaGateway(t, []byte("denied"))
	g.SetInbox((&Handler{workspace: workspace, inbox: ".inbox"}).InboxDir)

	g.status = http.StatusForbidden
	assert.Empty(t, g.handleImageMessage(context.Background(), message(`{"image_key":"img_key"}`), "oc_site"))
	assert.Contains(t, g.handleFileMessage(context.Background(), message(`{"file_key":"file_key","file_name":"a.pdf"}`), "oc_site", "file"), "下载失败")
	assert.NoDirExists(t, filepath.Join(workspace, ".inbox"))
}

func TestInboxSubdirRejectsUnsafeValues(t *testing.T) {
	for value, want := range map[string]string{
		"":                    "",
		"   ":                 "",
		".agents/tmp/inbox":   ".agents/tmp/inbox",
		"/.agents/tmp/inbox/": "",
		"../elsewhere":        "",
		"a/../../b":           "",
		"~/inbox":             "",
		"/etc/passwd":         "",
		"a/../inbox":          "",
		".":                   "",
		"inbox\nnew":          "",
		`C:\inbox`:            "",
	} {
		assert.Equal(t, want, inboxSubdir(value), "%q", value)
	}
}

func TestHandlerInboxDirFollowsTheChatWorkspace(t *testing.T) {
	workspace, project := t.TempDir(), t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	project, err = filepath.EvalSymlinks(project)
	require.NoError(t, err)
	handler := &Handler{workspace: workspace, inbox: ".agents/tmp/inbox"}
	assert.Equal(t, filepath.Join(workspace, ".agents", "tmp", "inbox"), handler.InboxDir("oc_unbound"))

	handler.routes = map[string]*ChatRoute{"oc_site": {ProjectRoot: project}}
	assert.Equal(t, filepath.Join(project, ".agents", "tmp", "inbox"), handler.InboxDir("oc_site"))

	// A bridge without an inbox, or without a workspace for the chat, has none.
	assert.Empty(t, (&Handler{workspace: workspace}).InboxDir("oc_site"))
	assert.Empty(t, (&Handler{inbox: "inbox"}).InboxDir("oc_site"))
}

func TestMediaInboxRejectsInvalidWorkspaceAndSymlinks(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "file"), []byte("x"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "escape")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "missing"), filepath.Join(ws, "dangling")))
	for _, tc := range []struct{ workspace, inbox string }{
		{"", "inbox"}, {"relative", "inbox"}, {filepath.Join(ws, "missing"), "inbox"},
		{filepath.Join(ws, "file"), "inbox"}, {ws, "escape/inbox"}, {ws, "dangling/deep/inbox"},
		{ws, "file/inbox"}, {ws, "../elsewhere"}, {ws, outside},
	} {
		h := &Handler{workspace: tc.workspace, inbox: tc.inbox}
		assert.Empty(t, h.InboxDir("chat"), "%+v", tc)
		g := newMediaGateway(t, []byte("x"))
		g.SetInbox(h.InboxDir)
		assert.Empty(t, g.handleImageMessage(context.Background(), message(`{"image_key":"x"}`), "chat"))
		assert.Empty(t, g.paths, "invalid inbox must not download or fall back to temp")
	}
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestMediaDownloadsUseUniqueSafeNames(t *testing.T) {
	ws := t.TempDir()
	h := &Handler{workspace: ws, inbox: "inbox"}
	g := newMediaGateway(t, []byte("new"))
	g.SetInbox(h.InboxDir)
	inbox := h.InboxDir("chat")
	require.NoError(t, os.MkdirAll(inbox, 0o750))
	victim := filepath.Join(ws, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("old"), 0o600))
	require.NoError(t, os.Symlink(victim, filepath.Join(inbox, "a.pdf")))
	require.NoError(t, os.Link(victim, filepath.Join(inbox, "b.pdf")))
	seen := map[string]bool{}
	for _, name := range []string{"a.pdf", "a.pdf", "b.pdf", "../../evil\n[link](x)`*.pdf"} {
		path, err := g.downloadResource(context.Background(), "chat", "msg", "key", "file", name)
		require.NoError(t, err)
		assert.False(t, seen[path])
		seen[path] = true
		assert.Equal(t, inbox, filepath.Dir(path))
		assert.NotContains(t, filepath.Base(path), "\n")
		assert.NotContains(t, filepath.Base(path), "[")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "new", string(data))
	}
	text := g.handleFileMessage(context.Background(), message(`{"file_key":"key","file_name":"bad\n[link](url).pdf"}`), "chat", "file")
	assert.Contains(t, text, "[文件: badlinkurl.pdf]")
	assert.NotContains(t, text, "\n")
	assert.NotContains(t, text, "[link]")
	data, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, "old", string(data))
}

type mediaTransport func(*http.Request) (*http.Response, error)

func (f mediaTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mediaTestBody struct {
	io.Reader
	closeErr error
}

func (b mediaTestBody) Close() error { return b.closeErr }

type mediaFillReader struct{}

func (mediaFillReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type mediaFailedReader struct{}

func (mediaFailedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestMediaDownloadLimitsAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.ReadCloser
		size int64
	}{
		{"declared oversized", io.NopCloser(strings.NewReader("")), maxMediaBytes + 1},
		{"stream oversized", io.NopCloser(io.LimitReader(mediaFillReader{}, maxMediaBytes+1)), -1},
		{"truncated", mediaTestBody{Reader: io.MultiReader(strings.NewReader("partial"), mediaFailedReader{})}, -1},
		{"close error", mediaTestBody{Reader: strings.NewReader("x"), closeErr: errors.New("close failed")}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newMediaGateway(t, nil)
			inbox := filepath.Join(t.TempDir(), "inbox")
			inboxParent, err := filepath.EvalSymlinks(filepath.Dir(inbox))
			require.NoError(t, err)
			inbox = filepath.Join(inboxParent, "inbox")
			g.SetInbox(func(string) string { return inbox })
			g.httpClient = &http.Client{Transport: mediaTransport(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), mediaDownloadTimeout)
				return &http.Response{StatusCode: http.StatusOK, Body: tc.body, ContentLength: tc.size, Header: make(http.Header)}, nil
			})}
			path, err := g.downloadResource(context.Background(), "chat", "msg", "key", "file", "a.pdf")
			require.Error(t, err)
			assert.Empty(t, path)
			entries, err := os.ReadDir(inbox)
			if !os.IsNotExist(err) {
				require.NoError(t, err)
				assert.Empty(t, entries, "partial downloads must be removed")
			}
		})
	}
}

func TestMediaDownloadHonorsContextTimeout(t *testing.T) {
	g := newMediaGateway(t, nil)
	g.SetInbox((&Handler{workspace: t.TempDir(), inbox: "inbox"}).InboxDir)
	g.httpClient = &http.Client{Transport: mediaTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	path, err := g.downloadResource(ctx, "chat", "msg", "key", "file", "a.pdf")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, path)
}

func TestMalformedImagesDoNotPromptTheModel(t *testing.T) {
	g := newMediaGateway(t, nil)
	for _, msg := range []*larkim.EventMessage{{}, message(`not json`), message(`{}`)} {
		assert.Empty(t, g.handleImageMessage(context.Background(), msg, "chat"))
	}
	assert.Empty(t, g.paths)
	g.status = http.StatusForbidden
	for _, content := range []string{
		`{"content":[[{"tag":"text","text":"caption"},{"tag":"img"}]]}`,
		`{"content":[[{"tag":"text","text":"caption"},{"tag":"img","image_key":"key"}]]}`,
	} {
		text, err := g.handlePostMessage(context.Background(), message(content), "chat")
		require.Error(t, err)
		assert.Empty(t, text, "failed images must not be forwarded as model prompts")
	}
}

func TestMediaDownloadAllowsExactSizeLimit(t *testing.T) {
	g := newMediaGateway(t, nil)
	g.SetInbox((&Handler{workspace: t.TempDir(), inbox: "inbox"}).InboxDir)
	g.httpClient = &http.Client{Transport: mediaTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.LimitReader(mediaFillReader{}, maxMediaBytes)), ContentLength: maxMediaBytes, Header: make(http.Header)}, nil
	})}
	path, err := g.downloadResource(context.Background(), "chat", "msg", "key", "file", "a.pdf")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.EqualValues(t, maxMediaBytes, info.Size())
}

type mediaContextReader struct{ ctx context.Context }

func (r mediaContextReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestMediaBodyTimeoutRemovesPartialFile(t *testing.T) {
	g := newMediaGateway(t, nil)
	h := &Handler{workspace: t.TempDir(), inbox: "inbox"}
	g.SetInbox(h.InboxDir)
	g.httpClient = &http.Client{Transport: mediaTransport(func(r *http.Request) (*http.Response, error) {
		body := io.MultiReader(strings.NewReader("partial"), mediaContextReader{r.Context()})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body), ContentLength: -1, Header: make(http.Header)}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	path, err := g.downloadResource(ctx, "chat", "msg", "key", "file", "a.pdf")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, path)
	entries, err := os.ReadDir(h.InboxDir("chat"))
	require.NoError(t, err)
	assert.Empty(t, entries)
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
