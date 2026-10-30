package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (a *preview) wallpaperSettings(c *ui.Context) {
	w := &a.Wallpaper
	p := a.Settings.Preferences.Background
	a.heading(c, "背景壁纸")
	a.note(c, "为工作区添加一张安静的背景。配色主题与壁纸可以独立选择。")
	ui.Row(c).Label("壁纸选项").Gap(10).Wrap().AlignItems(ui.Stretch).Children(func() {
		for _, preset := range wallpaperPresets {
			a.wallpaperChoice(c, preset.ID, preset.Label, preset.Image, false)
		}
		a.wallpaperChoice(c, "custom", "本地图片", w.Custom, w.Custom == nil)
	})
	ui.Row(c).Gap(10).Wrap().AlignItems(ui.Center).Children(func() {
		label := "选择图片"
		if p.CustomFile != "" {
			label = "更换图片"
		}
		a.action(c, label).Disabled(w.pending != nil).OnClick(a.pickWallpaper)
		if w.pending != nil {
			a.text(c, "正在读取图片…").FontSize(12).TextColor(c.Theme().TextMuted).Role(ui.RoleStatus)
		} else if p.CustomFile != "" {
			ui.Text(c, p.CustomName).Grow(1).MinWidth(0).SingleLine().FontSize(12).TextColor(c.Theme().TextMuted).Tooltip(p.CustomName)
			a.action(c, "移除图片").OnClick(func() {
				next := a.Settings.Preferences.Background
				next.CustomFile, next.CustomName = "", ""
				if next.Source == "custom" {
					next.Source = ""
				}
				if a.setWallpaperPreferences(next) {
					w.generation++
					w.Error = ""
				}
			})
		}
	})
	a.note(c, "支持 PNG、JPEG、WebP，最大 20 MB / 2400 万像素。图片会复制到本机应用目录，移动原图不影响背景。")
	if w.Error != "" {
		a.text(c, w.Error).Label("壁纸错误").Role(ui.RoleStatus).FontSize(12).TextColor(c.Theme().Danger)
	}
	if a.wallpaperImage() == nil {
		return
	}
	ui.Row(c).Gap(24).Wrap().Children(func() {
		a.wallpaperSlider(c, "背景强度", p.Strength, 100, func(value float64) {
			next := a.Settings.Preferences.Background
			next.Strength = value
			a.setWallpaperPreferences(next)
		})
		a.wallpaperSlider(c, "背景模糊", p.Blur, 16, func(value float64) {
			next := a.Settings.Preferences.Background
			next.Blur = value
			a.setWallpaperPreferences(next)
		})
	})
	a.note(c, "强度越高，背景越明显。界面始终保留阅读遮罩，模糊只作用于图片。")
}

func (a *preview) wallpaperChoice(c *ui.Context, id, label string, bitmap *ui.Bitmap, choose bool) {
	selected := a.Settings.Preferences.Background.Source == id
	e := ui.ButtonBase(c.Key("wallpaper/"+id)).Label(a.tr(label)).Basis(130).Grow(1).MinWidth(0).Column().AlignItems(ui.Stretch).Gap(8).Padding(6).Radius(8).Background(c.Theme().Surface).Border(1, c.Theme().Border).Selected(selected).Disabled(a.Wallpaper.pending != nil).OnClick(func() {
		if choose {
			a.pickWallpaper()
			return
		}
		next := a.Settings.Preferences.Background
		next.Source = id
		a.setWallpaperPreferences(next)
	})
	if selected {
		e.BorderColor(c.Theme().Accent).Background(c.Theme().Surface)
	} else if e.Hovered() {
		e.BorderColor(c.Theme().TextMuted)
	}
	e.Children(func() {
		ui.Box(c).FillWidth().Height(76).Radius(4).Clip().Background(c.Theme().Background).PassThrough().Children(func() {
			if bitmap != nil {
				ui.Image(c, bitmap).Absolute().Fill().Fit(ui.Cover).PassThrough()
			} else if id == "custom" {
				ui.Row(c).Fill().Center().Children(func() { icon(c, "plus") })
			}
		})
		ui.Row(c).Padding(0, 4, 2, 4).Gap(4).Children(func() {
			a.text(c, label).FontSize(12).TextColor(c.Theme().Text).SingleLine().MinWidth(0)
			ui.Spacer(c)
			if selected {
				ui.Icon(c, icons["circle-check"]).FontSize(15).TextColor(c.Theme().Accent)
			}
		})
	})
}

func (a *preview) wallpaperSlider(c *ui.Context, label string, value, maximum float64, update func(float64)) {
	ui.Column(c).Basis(210).Grow(1).MinWidth(0).Gap(8).Children(func() {
		ui.Row(c).Children(func() {
			a.text(c, label).FontSize(12)
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%.0f", value)).FontSize(12).TextColor(c.Theme().TextMuted)
		})
		if ui.Slider(c.Key(label), &value, 0, maximum).Step(1).Label(a.tr("调节" + label)).FillWidth().Changed() {
			update(value)
		}
	})
}
