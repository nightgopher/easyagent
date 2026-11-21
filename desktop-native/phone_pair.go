package main

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/camera"
	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"golang.org/x/image/draw"
)

type phonePairState struct {
	Code       string
	Camera     *camera.Camera
	Generation uint64
	Scanning   bool
	Failures   int
	Pending    *phonePairPending
}
type phonePairPending struct {
	Code  relayPairCode
	Token string
}

func (a *preview) closePairCamera() {
	p := &a.PhonePair
	p.Generation++
	p.Scanning = false
	if p.Camera != nil {
		p.Camera.Destroy()
		p.Camera = nil
	}
}
func (a *preview) beginPairCamera() {
	p := &a.PhonePair
	if p.Camera != nil || a.Window == nil {
		return
	}
	cam, err := camera.New(a.Window, camera.Options{})
	if err != nil {
		a.Live.Error = "无法打开相机，请检查相机权限"
		return
	}
	p.Camera = cam
	p.Failures = 0
	a.schedulePairCapture(p.Generation, 1500*time.Millisecond)
}

func (a *preview) schedulePairCapture(generation uint64, delay time.Duration) {
	time.AfterFunc(delay, func() {
		if a.Window == nil {
			return
		}
		a.Window.Update(func() {
			p := &a.PhonePair
			if p.Generation == generation && p.Camera != nil && p.Pending == nil {
				a.capturePairCode()
			}
		})
	})
}
func (a *preview) capturePairCode() {
	p := &a.PhonePair
	if p.Camera == nil || p.Scanning {
		return
	}
	p.Scanning = true
	cam := p.Camera
	generation := p.Generation
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		path, err := cam.TakePhoto(ctx)
		captureFailed := err != nil
		var raw string
		if err == nil {
			func() {
				defer os.Remove(path)
				f, e := os.Open(path)
				if e != nil {
					err = e
					return
				}
				defer f.Close()
				img, _, e := image.Decode(f)
				if e != nil {
					err = e
					return
				}
				raw, err = decodePairImage(img)
			}()
		}
		a.Window.Update(func() {
			if p.Generation != generation {
				return
			}
			p.Scanning = false
			if err != nil {
				if captureFailed {
					p.Failures++
					if p.Failures >= 5 {
						a.Live.Error = "无法读取相机，请检查权限后重新打开相机"
						a.closePairCamera()
						return
					}
				} else {
					p.Failures = 0
				}
				a.schedulePairCapture(generation, time.Second)
				return
			}
			if _, err = parseRelayPair(raw, time.Now()); err != nil {
				a.Live.Error = err.Error()
				a.schedulePairCapture(generation, 2*time.Second)
				return
			}
			p.Code = raw
			a.closePairCamera()
			a.claimPhonePair()
		})
	}()
}

// Downsample high-resolution camera frames: the preview is cropped while the
// captured sensor image is much larger, so try several sizes and harder search.
func decodePairImage(img image.Image) (string, error) {
	b := img.Bounds()
	if b.Empty() {
		return "", errors.New("empty image")
	}
	hints := map[gozxing.DecodeHintType]interface{}{gozxing.DecodeHintType_TRY_HARDER: true}
	for _, limit := range []int{2048, 1280, 768} {
		w, h := b.Dx(), b.Dy()
		if max(w, h) > limit {
			scale := float64(limit) / float64(max(w, h))
			w = max(1, int(float64(w)*scale))
			h = max(1, int(float64(h)*scale))
		}
		frame := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(frame, frame.Bounds(), img, b, draw.Src, nil)
		bitmap, err := gozxing.NewBinaryBitmapFromImage(frame)
		if err == nil {
			if result, e := qrcode.NewQRCodeReader().Decode(bitmap, hints); e == nil {
				return result.GetText(), nil
			}
		}
	}
	return "", errors.New("QR not found")
}

func (a *preview) claimPhonePair() {
	l, p := a.Live, &a.PhonePair
	if l.configBusy() {
		return
	}
	if p.Pending != nil {
		a.savePhonePair()
		return
	}
	code, err := parseRelayPair(p.Code, time.Now())
	if err != nil {
		l.Error = err.Error()
		return
	}
	l.Loading["host-save"] = true
	epoch := l.epoch
	go func() {
		ctx, cancel := context.WithTimeout(l.ctx, 45*time.Second)
		defer cancel()
		b, err := newRelayBackend(code.relayConfig, "", websocket.DefaultDialer)
		var result struct {
			Token string `json:"device_token"`
		}
		if err == nil {
			err = b.request(ctx, "POST", "/pair/claim", map[string]string{"pair_token": code.PairToken, "name": "EasyAgent Android"}, &result)
		}
		if err == nil && !relaySecretPattern.MatchString(result.Token) {
			err = errors.New("电脑返回的设备凭据无效")
		}
		a.post(epoch, func() {
			l.Loading["host-save"] = false
			if err != nil {
				l.Error = err.Error()
				return
			}
			code.PairToken = ""
			p.Code = ""
			p.Pending = &phonePairPending{Code: code, Token: result.Token}
			a.savePhonePair()
		})
	}()
}
func (a *preview) savePhonePair() {
	l, p := a.Live, &a.PhonePair
	if p.Pending == nil || l.configBusy() {
		return
	}
	pending := *p.Pending
	old := cloneConfig(l.Config)
	l.Loading["host-save"] = true
	epoch := l.epoch
	go func() {
		host := hostConfig{ID: requestID(), Name: strings.TrimSpace(pending.Code.HostName), HasToken: true, Relay: &pending.Code.relayConfig}
		if host.Name == "" {
			host.Name = "电脑"
		}
		if len([]rune(host.Name)) > 128 {
			host.Name = string([]rune(host.Name)[:128])
		}
		name, _, err := host.Relay.validate()
		host.URL = "https://" + name
		if err == nil {
			err = verifiedHostCredential(l.Secrets, host.ID, pending.Token)
			if err != nil {
				_ = l.Secrets.Delete("host/" + host.ID)
			}
		}
		if err == nil {
			old.Hosts = append(old.Hosts, host)
			err = atomicJSON(l.Dir+"/connections.json", old)
			if err != nil {
				_ = l.Secrets.Delete("host/" + host.ID)
			}
		}
		a.post(epoch, func() {
			l.Loading["host-save"] = false
			if err != nil {
				l.Error = "电脑已完成配对，但保存失败；请重试保存"
				return
			}
			p.Pending = nil
			l.Config = old
			l.Error = ""
			a.syncSettingsConfig()
			a.selectLiveHost(len(old.Hosts) - 1)
			a.closeSettings()
			a.Sidebar = true
		})
	}()
}
func (a *preview) phonePairView(c *ui.Context) {
	p, l := &a.PhonePair, a.Live
	ui.Column(c).FillWidth().Gap(10).Padding(14).Radius(10).Background(c.Theme().Surface).Children(func() {
		a.heading(c, "扫码配对电脑")
		a.note(c, "在电脑的设置 → 手机远程访问中生成二维码。")
		if p.Pending != nil {
			a.note(c, "电脑已配对，正在等待保存手机凭据。")
			ui.Row(c).Gap(8).Children(func() {
				a.primary(c, "重试保存").Disabled(l.Loading["host-save"]).OnClick(a.savePhonePair)
				a.action(c, "放弃配对").Disabled(l.Loading["host-save"]).OnClick(func() {
					l.Confirm = &nativeConfirmation{"放弃未保存的配对？", "电脑已登记此设备。放弃后请在电脑的手机远程访问设置中撤销该设备，再重新配对。", func() { p.Pending = nil }}
				})
			})
			return
		}
		if p.Camera != nil {
			ui.Box(c).FillWidth().Height(240).Children(func() { ui.WebView(c, p.Camera).Fill() })
			ui.Row(c).Gap(8).Children(func() {
				a.note(c, "将电脑二维码放入画面，识别后自动连接。")
				a.action(c, "关闭相机").OnClick(a.closePairCamera)
			})
		} else {
			a.primary(c, "打开相机扫码").Disabled(l.Loading["host-save"]).OnClick(a.beginPairCamera)
		}
		a.input(c, "粘贴配对码", &p.Code, "从电脑复制配对码", true)
		a.action(c, "配对并连接").Disabled(l.Loading["host-save"] || strings.TrimSpace(p.Code) == "").OnClick(a.claimPhonePair)
	})
}
