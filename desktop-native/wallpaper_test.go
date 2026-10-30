package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func wallpaperPreview(t *testing.T) *preview {
	t.Helper()
	a := newPreview()
	a.Settings.Path = filepath.Join(t.TempDir(), "preferences.json")
	return a
}

func awaitWallpaper(t *testing.T, a *preview) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Wallpaper.pending != nil && time.Now().Before(deadline) {
		a.wallpaperTick()
		time.Sleep(time.Millisecond)
	}
	if a.Wallpaper.pending != nil {
		t.Fatal("wallpaper read did not complete")
	}
}

func TestWallpaperImportSurvivesMovingOriginalAndRestart(t *testing.T) {
	a := wallpaperPreview(t)
	original := filepath.Join(t.TempDir(), "我的背景.webp")
	if err := os.WriteFile(original, mistWallpaper, 0600); err != nil {
		t.Fatal(err)
	}
	a.Wallpaper.pick = func(o mygo.OpenDialogOptions) ([]string, error) {
		if o.Multiple || len(o.Filters) != 1 {
			t.Error("wallpaper picker must select one image")
		}
		return []string{original}, nil
	}
	a.pickWallpaper()
	awaitWallpaper(t, a)
	p := a.Settings.Preferences.Background
	if p.Source != "custom" || p.CustomName != "我的背景.webp" || a.wallpaperImage() == nil || a.Wallpaper.Error != "" {
		t.Fatalf("import failed: %+v, %s", p, a.Wallpaper.Error)
	}
	saved := a.wallpaperPath(p.CustomFile)
	data, err := os.ReadFile(saved)
	if err != nil || !bytes.Equal(data, mistWallpaper) {
		t.Fatalf("owned image differs: %v", err)
	}
	info, err := os.Stat(saved)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("image must be private: %v", err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	b := newPreview()
	b.Settings.loadPreferences(a.Settings.Path)
	b.loadWallpaper()
	awaitWallpaper(t, b)
	if b.wallpaperImage() == nil || b.Settings.Preferences.Background != p {
		t.Fatalf("wallpaper lost after restart: %+v %s", b.Settings.Preferences, b.Wallpaper.Error)
	}
	bitmap := b.wallpaperImage()
	b.Settings.Preferences.Theme = "light"
	b.Settings.selectHost(1)
	if b.wallpaperImage() != bitmap {
		t.Fatal("theme/host changes must reuse the decoded image")
	}
	if !b.setWallpaperPreferences(defaultWallpaperPreferences()) {
		t.Fatal(b.Settings.Error)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) || b.Wallpaper.Custom != nil {
		t.Fatal("reset must remove only the imported copy")
	}
}

func TestWallpaperInvalidInputsAndMissingImage(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "not-an-image.png")
	if err := os.WriteFile(invalid, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, invalid, filepath.Join(dir, "missing.png")} {
		if r := readWallpaper(path); r.Err == nil || r.Image != nil {
			t.Errorf("accepted invalid image %s", path)
		}
	}
	large, err := os.Create(filepath.Join(dir, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(maxWallpaperBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	if r := readWallpaper(large.Name()); r.Err == nil || !strings.Contains(r.Err.Error(), "20 MB") {
		t.Fatal("oversized file was not rejected before decoding")
	}
	a := wallpaperPreview(t)
	p := wallpaperPreferences{Source: "custom", CustomFile: strings.Repeat("a", 64) + ".image", Strength: 60}
	if !a.setWallpaperPreferences(p) {
		t.Fatal(a.Settings.Error)
	}
	a.loadWallpaper()
	awaitWallpaper(t, a)
	if a.wallpaperImage() != nil || a.Wallpaper.Error == "" || a.Settings.Preferences.Background != p {
		t.Fatal("missing image should fall back visually and retain recoverable settings")
	}
}

func TestWallpaperLegacyPreferencesAndPathValidation(t *testing.T) {
	a := wallpaperPreview(t)
	if err := os.WriteFile(a.Settings.Path, []byte(`{"language":"English","theme":"light","workbench_width":500}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.Settings.loadPreferences(a.Settings.Path)
	if a.Settings.Preferences.Background != defaultWallpaperPreferences() || a.Settings.Preferences.WorkbenchWidth != 500 {
		t.Fatal("legacy preference migration lost settings")
	}
	for _, name := range []string{"../secret", "/tmp/background.png", "..\\secret", "", strings.Repeat("z", 64) + ".image"} {
		p := normalizeWallpaper(wallpaperPreferences{Source: "custom", CustomFile: name, Strength: 999, Blur: -1})
		if p.Source != "" || p.CustomFile != "" || p.Strength != 100 || p.Blur != 0 || a.wallpaperPath(name) != "" {
			t.Fatalf("invalid preference not normalized: %+v", p)
		}
	}
	for _, query := range []string{"壁纸", "background", "模糊"} {
		if !settingsPageMatches(settingsPages[1], query) {
			t.Errorf("wallpaper cannot be found with %s", query)
		}
	}
}

func TestWallpaperImportFailureAndCancellationPreservePrevious(t *testing.T) {
	a := wallpaperPreview(t)
	p := defaultWallpaperPreferences()
	p.Source = "mist"
	if !a.setWallpaperPreferences(p) {
		t.Fatal(a.Settings.Error)
	}
	a.Wallpaper.pick = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	a.pickWallpaper()
	awaitWallpaper(t, a)
	if a.Settings.Preferences.Background != p || a.Wallpaper.Error != "" {
		t.Fatal("cancelled picker changed settings")
	}
	// A read started before restoring defaults must never re-apply the old image.
	a.startWallpaperRead(func() wallpaperResult {
		return wallpaperResult{Image: wallpaperPresets[1].Image, Data: mistWallpaper, Import: true, Name: "mist.webp"}
	})
	a.resetPageDefaults("appearance")
	a.Settings.Confirm.Action()
	awaitWallpaper(t, a)
	if a.wallpaperImage() != nil || a.Settings.Preferences.Background != defaultWallpaperPreferences() {
		t.Fatal("late picker result overwrote reset")
	}
	// Fail the final preference rename after the private image copy succeeds.
	if err := os.Remove(a.Settings.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.Settings.Path, 0700); err != nil {
		t.Fatal(err)
	}
	before := a.Settings.Preferences.Background
	err := a.installWallpaper(wallpaperResult{Image: wallpaperPresets[1].Image, Data: mistWallpaper, Name: "mist.webp"})
	files, readErr := os.ReadDir(filepath.Join(filepath.Dir(a.Settings.Path), "wallpapers"))
	if err == nil || readErr != nil || len(files) != 0 || a.Settings.Preferences.Background != before {
		t.Fatalf("failed import changed background or leaked an image: %v %v %v", err, readErr, files)
	}
}

func TestWallpaperControlsRenderingAndPersistence(t *testing.T) {
	for _, width := range []int{640, 900, 1240} {
		for _, language := range []string{"中文", "English"} {
			t.Run(fmt.Sprintf("%d-%s", width, language), func(t *testing.T) {
				a := wallpaperPreview(t)
				a.Settings.Preferences.Language = language
				a.openSettings("appearance")
				tt := ui.NewTester(a.View, width, 1000)
				for _, label := range []string{"纯色", "山间薄雾", "暮色海面", "本地图片", "选择图片"} {
					r, ok := tt.Find(a.tr(label))
					if !ok || r.W < 60 || r.X < 240 || r.X+r.W > float32(width) {
						t.Fatalf("wallpaper control overflows: %s %+v", label, r)
					}
				}
				click(t, tt, a.tr("山间薄雾"))
				if a.Settings.Preferences.Background.Source != "mist" {
					t.Fatal("thumbnail is not clickable")
				}
				if _, ok := tt.Find(a.tr("调节背景强度")); !ok {
					t.Fatal("wallpaper controls missing")
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("wallpaper-settings-%d-%s.png", width, language)))
				}
				// Keyboard control is available without a pointer drag.
				click(t, tt, a.tr("调节背景强度"))
				before := a.Settings.Preferences.Background.Strength
				tt.Key(0, ui.KeyRight)
				if a.Settings.Preferences.Background.Strength <= before {
					t.Fatal("keyboard adjustment did not change wallpaper strength")
				}
				restored := newSettings()
				restored.loadPreferences(a.Settings.Path)
				if restored.Preferences.Background != a.Settings.Preferences.Background {
					t.Fatal("strength was not persisted")
				}
				click(t, tt, a.tr("浅色"))
				if a.Settings.Preferences.Background.Source != "mist" {
					t.Fatal("theme selection cleared wallpaper")
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("wallpaper-settings-light-%d-%s.png", width, language)))
				}
				click(t, tt, a.tr("纯色"))
				if a.wallpaperImage() != nil {
					t.Fatal("solid color did not disable wallpaper")
				}
			})
		}
	}
}

func TestWallpaperDoesNotCoverContentOrInterceptInput(t *testing.T) {
	a := wallpaperPreview(t)
	a.Sidebar = false
	a.current().Messages = nil
	p := defaultWallpaperPreferences()
	p.Source, p.Strength = "mist", 100
	if !a.setWallpaperPreferences(p) {
		t.Fatal(a.Settings.Error)
	}
	for _, theme := range []string{"dark", "light"} {
		a.Settings.Preferences.Theme = theme
		a.Sidebar = true
		tt := ui.NewTester(a.View, 1240, 820)
		click(t, tt, "消息输入")
		tt.Type("wallpaper typing test")
		if a.current().Draft == "" {
			t.Fatal("wallpaper intercepted keyboard/pointer input")
		}
		withImage := tt.Image().RGBAAt(600, 300)
		sideWithImage := tt.Image().RGBAAt(40, 650)
		a.Settings.Preferences.Background.Source = ""
		tt.Frame()
		withoutImage := tt.Image().RGBAAt(600, 300)
		if sideWithoutImage := tt.Image().RGBAAt(40, 650); sideWithoutImage != sideWithImage {
			t.Fatal("wallpaper was painted over the opaque sidebar")
		}
		if withImage == withoutImage {
			t.Fatal("wallpaper is hidden behind an opaque root")
		}
		a.Settings.Preferences.Background.Source = "mist"
		a.current().Messages = []*message{{Text: "背景保留安静的层次，文字依然清楚。\n\n```go\nfmt.Println(\"EasyAgent\")\n```"}}
		tt.Frame()
		if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
			saveSnapshot(t, tt, filepath.Join(dir, "wallpaper-chat-"+theme+".png"))
		}
	}
}

func TestWallpaperLoadIndependentOfHostEpoch(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Path = filepath.Join(t.TempDir(), "preferences.json")
	a.startWallpaperRead(func() wallpaperResult {
		return wallpaperResult{Image: wallpaperPresets[1].Image, Data: mistWallpaper, Name: "mist.webp", Import: true}
	})
	a.Live.epoch++
	awaitWallpaper(t, a)
	if a.Settings.Preferences.Background.Source != "custom" || a.wallpaperImage() == nil {
		t.Fatal("changing hosts discarded the device-local wallpaper")
	}
}

func TestWallpaperBlurLeavesForegroundUntouched(t *testing.T) {
	a := wallpaperPreview(t)
	a.Settings.Preferences.Background = wallpaperPreferences{Source: "mist", Strength: 100}
	tt := ui.NewTester(a.View, 1240, 820)
	sharp := tt.Image().RGBAAt(650, 450)
	foreground := tt.Image().RGBAAt(40, 650)
	a.Settings.Preferences.Background.Blur = 16
	tt.Frame()
	if tt.Image().RGBAAt(650, 450) == sharp {
		t.Fatal("blur did not affect the wallpaper")
	}
	if tt.Image().RGBAAt(40, 650) != foreground {
		t.Fatal("blur affected the foreground sidebar")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "wallpaper-blur.png"))
	}
}

func BenchmarkWallpaperFrame(b *testing.B) {
	for _, blur := range []float64{0, 8, 16} {
		b.Run(fmt.Sprint(blur), func(b *testing.B) {
			a := newPreview()
			a.Settings.Preferences.Background = wallpaperPreferences{Source: "mist", Strength: 60, Blur: blur}
			tt := ui.NewTester(a.View, 1240, 820)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tt.Frame()
			}
		})
	}
}

func TestWallpaperTextContrastAtMaximumStrength(t *testing.T) {
	luminance := func(c ui.Color) float64 {
		var l float64
		for i, v := range []uint8{c.R, c.G, c.B} {
			s := float64(v) / 255
			if s <= .04045 {
				s /= 12.92
			} else {
				s = math.Pow((s+.055)/1.055, 2.4)
			}
			l += s * []float64{.2126, .7152, .0722}[i]
		}
		return l
	}
	for _, mode := range []string{"dark", "light"} {
		prefs := localPreferences{Theme: mode, Background: wallpaperPreferences{Source: "mist", Strength: 100}}
		theme := previewTheme(&ui.Theme{}, prefs)
		worst := ui.Hex("#000000")
		if theme.Dark {
			worst = ui.Hex("#ffffff")
		}
		background := theme.Background.Mix(worst, wallpaperOpacity(prefs.Background))
		for _, text := range []ui.Color{theme.Text, theme.TextMuted} {
			a, b := luminance(background), luminance(text)
			if contrast := (max(a, b) + .05) / (min(a, b) + .05); contrast < 4.5 {
				t.Errorf("%s contrast %.2f falls below 4.5:1", mode, contrast)
			}
		}
	}
}

func TestWallpaperRejectsDecodedPixelBomb(t *testing.T) {
	// A highly compressible image is small on disk, but expensive to decode.
	img := image.NewPaletted(image.Rect(0, 0, 6000, 4001), color.Palette{color.Black})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "huge.png")
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if r := readWallpaper(path); r.Err == nil || !strings.Contains(r.Err.Error(), "2400 万") {
		t.Fatal("pixel limit was not checked before allocating a bitmap")
	}
}
