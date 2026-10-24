package main

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
)

var quotedFilePath = regexp.MustCompile("`((?:/|~/|[A-Za-z]:[\\\\/])[^`\\n]+)`")

func markdownFileReferences(source string) string {
	lines := strings.Split(source, "\n")
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		lines[i] = quotedFilePath.ReplaceAllStringFunc(line, func(token string) string {
			value := strings.Trim(token, "`")
			if strings.ContainsAny(value, "\x00\r\n") {
				return token
			}
			return "[" + token + "](ea-file:" + url.PathEscape(value) + ")"
		})
	}
	return strings.Join(lines, "\n")
}

// resolveMarkdownFile turns a link target into an absolute path on the active host. "~/" is expanded with
// home, the host's own home directory as reported by the host, never the client machine's.
func resolveMarkdownFile(workspace, home, base, href string) (string, error) {
	if strings.HasPrefix(href, "ea-file:") {
		var err error
		href, err = url.PathUnescape(strings.TrimPrefix(href, "ea-file:"))
		if err != nil {
			return "", err
		}
	}
	if strings.ContainsAny(href, "\x00\r\n") || strings.HasPrefix(href, "//") {
		return "", errors.New("文件路径无效")
	}
	if u, err := url.Parse(href); err == nil && u.Scheme != "" && !(len(u.Scheme) == 1 && len(href) > 2 && href[1] == ':') {
		return "", errors.New("文件链接协议无效")
	}
	href = strings.ReplaceAll(href, "\\", "/")
	if strings.HasPrefix(href, "~/") {
		home = strings.ReplaceAll(home, "\\", "/")
		if !strings.HasPrefix(home, "/") && !(len(home) > 2 && home[1] == ':') {
			return "", errors.New("无法确定当前主机的家目录，请升级该主机上的 EasyAgent 后重试")
		}
		href = path.Join(home, href[2:])
	}
	if !strings.HasPrefix(href, "/") && !(len(href) > 2 && href[1] == ':') {
		directory := strings.ReplaceAll(workspace, "\\", "/")
		if base != "" {
			directory = path.Dir(strings.ReplaceAll(base, "\\", "/"))
		}
		href = path.Join(directory, href)
	}
	return path.Clean(href), nil
}

// pathInsideWorkspace reports whether target lies in workspace. Both are absolute paths on the host;
// the comparison is lexical, like the host's own check, and case-insensitive for Windows drive paths.
func pathInsideWorkspace(workspace, target string) bool {
	root := strings.TrimRight(path.Clean(strings.ReplaceAll(workspace, "\\", "/")), "/")
	target = path.Clean(strings.ReplaceAll(target, "\\", "/"))
	if len(root) > 1 && root[1] == ':' {
		root, target = strings.ToLower(root), strings.ToLower(target)
	}
	return target == root || strings.HasPrefix(target, root+"/")
}

func (l *liveState) activeHostLocal() bool {
	for _, h := range l.Config.Hosts {
		if h.ID == l.Config.ActiveHost {
			return h.Local
		}
	}
	return false
}

// hostHome is the home directory of the host serving s. The host reports it; the local host falls
// back to this machine's own home, which is the same directory.
func (a *preview) hostHome(s *session) string {
	if s.Home != "" {
		return s.Home
	}
	if a.Live != nil && a.Live.activeHostLocal() {
		home, _ := os.UserHomeDir()
		return home
	}
	return ""
}

// openLocalDirectory shows a folder in the system file manager; tests replace it.
var openLocalDirectory = func(dir string) error { return mygo.Shell.OpenPath(dir) }

// revealOutsideWorkspace handles a link to a file the host will not serve because it is outside the
// session workspace. On this machine the file's folder is opened, never the file itself: the path
// comes from model output, so launching it could run a program.
func (a *preview) revealOutsideWorkspace(target string) {
	if !a.Live.activeHostLocal() {
		a.Live.Error = "该文件不在会话工作区内，无法在远程主机上预览"
		return
	}
	a.showInFileManager(target)
}

// showInFileManager opens the folder holding target (or target itself when it is a folder) in the
// system file manager. Only meaningful for paths on this machine.
func (a *preview) showInFileManager(target string) {
	native := filepath.FromSlash(target)
	info, err := os.Stat(native)
	if err != nil {
		a.Live.Error = "文件不存在：" + target
		return
	}
	if !info.IsDir() {
		native = filepath.Dir(native)
	}
	go openLocalDirectory(native)
}
func decodeNativeImage(data []byte, name string) (ui.ImageSource, error) {
	if len(data) > 8<<20 {
		return nil, errors.New("图片超过 8 MiB")
	}
	if strings.EqualFold(path.Ext(name), ".svg") || strings.Contains(string(data[:min(len(data), 300)]), "<svg") {
		return ui.ParseSVG(data)
	}
	return ui.DecodeBitmap(data)
}
func (a *preview) renderSessionMarkdown(c *ui.Context, d *markdownDocument, text string, s *session, base string, streaming bool) {
	if a.Live == nil {
		d.render(c, text, streaming)
		return
	}
	l := a.Live
	home := a.hostHome(s)
	scope := strconv.FormatUint(l.epoch, 10) + "\x00" + l.Config.ActiveHost + "\x00" + s.RemoteID + "\x00" + s.Workspace + "\x00" + home + "\x00" + base
	if d.MediaScope != scope {
		d.MediaScope = scope
		d.options = markdown.Options{}
		d.ready = false
		ctx, b, sid, workspace := l.hostCtx, l.Client, s.RemoteID, s.Workspace
		d.options.OnLink = func(href string) {
			if safeMarkdownLink(href) {
				go mygo.Shell.OpenExternal(href)
				return
			}
			if strings.HasPrefix(href, "#") {
				d.Anchor = strings.ToLower(strings.TrimPrefix(href, "#"))
				return
			}
			target, err := resolveMarkdownFile(workspace, home, base, href)
			if err != nil {
				l.Error = err.Error()
				return
			}
			if a.current() != s {
				return
			}
			// The host only serves files inside the session workspace, so anything else is
			// shown in the local file manager (this machine only) instead of failing to open.
			if workspace != "" && !pathInsideWorkspace(workspace, target) {
				a.revealOutsideWorkspace(target)
				return
			}
			a.openResource("files")
			a.openNativeFile(target)
		}
		d.options.Block = func(c *ui.Context, doc *parser.Document, index int) bool {
			if doc.Block(index).Kind == parser.Heading && d.Anchor != "" && markdown.Slug(parser.PlainText(doc.Inlines(index))) == d.Anchor {
				ui.Box(c).Height(0).ScrollIntoView()
				d.Anchor = ""
			}
			return false
		}
		d.options.LoadImage = func(href string) (ui.ImageSource, error) {
			if strings.HasPrefix(href, "data:image/") {
				meta, encoded, ok := strings.Cut(href, ",")
				if !ok || !strings.HasSuffix(meta, ";base64") || len(encoded) > 12<<20 {
					return nil, errors.New("图片数据无效")
				}
				data, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return nil, err
				}
				return decodeNativeImage(data, meta)
			}
			if u, err := url.Parse(href); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil {
				request, err := http.NewRequestWithContext(ctx, "GET", href, nil)
				if err != nil {
					return nil, err
				}
				client := http.Client{Timeout: 20 * time.Second}
				response, err := client.Do(request)
				if err != nil {
					return nil, err
				}
				defer response.Body.Close()
				if response.StatusCode != 200 {
					return nil, errors.New("图片无法读取")
				}
				data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
				if err != nil {
					return nil, err
				}
				return decodeNativeImage(data, u.Path)
			}
			target, err := resolveMarkdownFile(workspace, home, base, href)
			if err != nil {
				return nil, err
			}
			if b == nil || sid == "" {
				return nil, errors.New("未连接工作区")
			}
			var result map[string]any
			if err = b.request(ctx, http.MethodGet, a.workspacePath("file-data", target, sid), nil, &result); err != nil {
				return nil, err
			}
			data, err := base64.StdEncoding.DecodeString(str(result["data"]))
			if err != nil {
				return nil, err
			}
			return decodeNativeImage(data, target)
		}
	}
	d.render(c, markdownFileReferences(text), streaming)
}
