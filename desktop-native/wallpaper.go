package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

//go:embed wallpapers/mist.webp
var mistWallpaper []byte

//go:embed wallpapers/dusk.webp
var duskWallpaper []byte

// Decode packaged assets once, never during a frame. Custom images are decoded
// off the UI thread and retained across host, session and theme changes too.
var wallpaperPresets = []struct {
	ID, Label string
	Image     *ui.Bitmap
}{
	{"", "纯色", nil},
	{"mist", "山间薄雾", packagedWallpaper(mistWallpaper)},
	{"dusk", "暮色海面", packagedWallpaper(duskWallpaper)},
}

func packagedWallpaper(data []byte) *ui.Bitmap {
	b, err := ui.DecodeBitmap(data)
	if err != nil {
		panic("invalid packaged wallpaper: " + err.Error())
	}
	return b
}

type wallpaperPreferences struct {
	Source     string  `json:"source,omitempty"`
	CustomFile string  `json:"custom_file,omitempty"`
	CustomName string  `json:"custom_name,omitempty"`
	Strength   float64 `json:"strength"`
	Blur       float64 `json:"blur"`
}

func defaultWallpaperPreferences() wallpaperPreferences {
	return wallpaperPreferences{Strength: 60}
}

func normalizeWallpaper(p wallpaperPreferences) wallpaperPreferences {
	switch p.Source {
	case "", "mist", "dusk", "custom":
	default:
		p.Source = ""
	}
	if !validWallpaperFile(p.CustomFile) {
		p.CustomFile, p.CustomName = "", ""
	}
	if p.Source == "custom" && p.CustomFile == "" {
		p.Source = ""
	}
	if math.IsNaN(p.Strength) || math.IsInf(p.Strength, 0) {
		p.Strength = 60
	}
	if math.IsNaN(p.Blur) || math.IsInf(p.Blur, 0) {
		p.Blur = 0
	}
	p.Strength = max(0, min(100, p.Strength))
	p.Blur = max(0, min(16, p.Blur))
	return p
}

func validWallpaperFile(name string) bool {
	if len(name) != 64+len(".image") || !strings.HasSuffix(name, ".image") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".image"))
	return err == nil
}

type wallpaperState struct {
	Custom     *ui.Bitmap
	Error      string
	pending    <-chan wallpaperResult
	generation uint64
	pick       func(mygo.OpenDialogOptions) ([]string, error)
}

type wallpaperResult struct {
	Image      *ui.Bitmap
	Data       []byte
	Name       string
	Import     bool
	Generation uint64
	Err        error
}

const maxWallpaperBytes = 20 << 20
const maxWallpaperPixels = 24_000_000

func readWallpaper(path string) wallpaperResult {
	r := wallpaperResult{Name: filepath.Base(path)}
	f, err := os.Open(path)
	if err != nil {
		r.Err = errors.New("无法读取背景图片，请重新选择。")
		return r
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		r.Err = errors.New("请选择图片文件。")
		return r
	}
	if info.Size() > maxWallpaperBytes {
		r.Err = errors.New("图片不能超过 20 MB。")
		return r
	}
	r.Data, err = io.ReadAll(io.LimitReader(f, maxWallpaperBytes+1))
	if err != nil || len(r.Data) > maxWallpaperBytes {
		r.Err = errors.New("无法读取背景图片，请选择不超过 20 MB 的图片。")
		return r
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(r.Data))
	if err != nil {
		r.Err = errors.New("图片无法解析，请选择 PNG、JPEG 或 WebP 图片。")
		return r
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxWallpaperPixels {
		r.Err = errors.New("图片分辨率过大，请选择不超过 2400 万像素的图片。")
		return r
	}
	r.Image, err = ui.DecodeBitmap(r.Data) // also respects JPEG EXIF orientation
	if err != nil {
		r.Err = errors.New("图片无法解析，请选择 PNG、JPEG 或 WebP 图片。")
	}
	return r
}

func (a *preview) wallpaperPath(name string) string {
	if a.Settings.Path == "" || !validWallpaperFile(name) {
		return ""
	}
	return filepath.Join(filepath.Dir(a.Settings.Path), "wallpapers", name)
}

func (a *preview) loadWallpaper() {
	path := a.wallpaperPath(a.Settings.Preferences.Background.CustomFile)
	if path == "" {
		return
	}
	a.startWallpaperRead(func() wallpaperResult { return readWallpaper(path) })
}

func (a *preview) startWallpaperRead(work func() wallpaperResult) {
	w := &a.Wallpaper
	w.generation++
	generation := w.generation
	results := make(chan wallpaperResult, 1)
	w.pending, w.Error = results, ""
	go func() {
		r := work()
		r.Generation = generation
		results <- r
	}()
}

func (a *preview) pickWallpaper() {
	if a.Wallpaper.pending != nil {
		return
	}
	choose := a.Wallpaper.pick
	if choose == nil {
		choose = mygo.Dialog.Open
	}
	options := mygo.OpenDialogOptions{Parent: a.Window, Title: a.tr("选择背景图片"), Filters: []mygo.FileFilter{{Name: a.tr("图片"), Extensions: []string{"png", "jpg", "jpeg", "webp"}}}}
	a.startWallpaperRead(func() wallpaperResult {
		paths, err := choose(options)
		if err != nil {
			return wallpaperResult{Err: errors.New("无法打开图片选择器，请重试。")}
		}
		if len(paths) == 0 { // Cancelling keeps the current wallpaper and preferences.
			return wallpaperResult{}
		}
		r := readWallpaper(paths[0])
		r.Import = true
		return r
	})
}

// Wallpaper work is device-local, so it must not use liveState's host epoch.
// Switching servers while the picker is open cannot discard its result.
func (a *preview) wallpaperTick() bool {
	w := &a.Wallpaper
	select {
	case r := <-w.pending:
		w.pending = nil
		if r.Generation != w.generation {
			return true
		}
		if r.Err != nil {
			w.Error = r.Err.Error()
			return true
		}
		if r.Image == nil {
			return true
		}
		if r.Import {
			if err := a.installWallpaper(r); err != nil {
				w.Error = err.Error()
				return true
			}
		}
		w.Custom, w.Error = r.Image, ""
		return true
	default:
		return false
	}
}

func (a *preview) installWallpaper(r wallpaperResult) error {
	name := fmt.Sprintf("%x.image", sha256.Sum256(r.Data))
	path := a.wallpaperPath(name)
	if path == "" {
		return errors.New("无法保存背景图片，请检查应用数据目录。")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("无法保存背景图片，请检查应用数据目录。")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".import-*")
	if err != nil {
		return errors.New("无法保存背景图片，请检查应用数据目录。")
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(r.Data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("无法保存背景图片，请检查应用数据目录。")
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return errors.New("无法保存背景图片，请检查应用数据目录。")
	}
	old := a.Settings.Preferences.Background
	p := old
	p.Source, p.CustomFile, p.CustomName = "custom", name, r.Name
	if !a.setWallpaperPreferences(p) {
		if old.CustomFile != name {
			_ = os.Remove(path)
		}
		return errors.New("背景设置保存失败，已保留原背景。")
	}
	return nil
}

func (a *preview) setWallpaperPreferences(p wallpaperPreferences) bool {
	s := &a.Settings
	old := s.Preferences.Background
	s.Preferences.Background = normalizeWallpaper(p)
	if !s.savePreferences() {
		s.Preferences.Background = old
		return false
	}
	if old.CustomFile != s.Preferences.Background.CustomFile {
		if path := a.wallpaperPath(old.CustomFile); path != "" {
			_ = os.Remove(path) // Only an app-owned, content-addressed image.
		}
		a.Wallpaper.Custom = nil
	}
	return true
}

func (a *preview) wallpaperImage() *ui.Bitmap {
	source := a.Settings.Preferences.Background.Source
	if source == "custom" {
		return a.Wallpaper.Custom
	}
	for _, preset := range wallpaperPresets {
		if preset.ID == source {
			return preset.Image
		}
	}
	return nil
}

// The strength control is relative to a readable maximum. At 100%, the theme
// still supplies a 72% scrim; body and secondary text remain above 4.5:1 even
// over a completely white (dark theme) or black (light theme) photograph.
func wallpaperOpacity(p wallpaperPreferences) float32 {
	return float32(normalizeWallpaper(p).Strength) * .0028
}

func (a *preview) wallpaperLayer(c *ui.Context) {
	if bitmap := a.wallpaperImage(); bitmap != nil {
		p := a.Settings.Preferences.Background
		ui.Image(c, bitmap).Absolute().Fill().Fit(ui.Cover).Blur(float32(p.Blur)).Opacity(wallpaperOpacity(p)).PassThrough()
	}
}
