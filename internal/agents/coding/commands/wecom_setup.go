package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/nightgopher/easyagent/internal/wecom"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

var wecomSetupUsage = "用法：/wecom setup\n扫码或复制链接确认接入；二维码在终端渲染，链接可在其他设备打开。"

type wecomSetupServices struct {
	create func(context.Context) (*wecom.QrSession, error)
	wait   func(context.Context, *wecom.QrSession) (wecom.Bot, error)
	save   func(wecom.Credentials) error
}

func defaultWecomSetupServices() wecomSetupServices {
	return wecomSetupServices{
		create: wecom.CreateQrSession,
		wait:   func(ctx context.Context, s *wecom.QrSession) (wecom.Bot, error) { return s.Wait(ctx) },
		save:   wecom.SaveCredentials,
	}
}

func handleWecomSetup(ctx slashcmd.Context) (slashcmd.CommandResult, error) {
	return runWecomSetup(ctx, defaultWecomSetupServices())
}

func runWecomSetup(cmd slashcmd.Context, services wecomSetupServices) (slashcmd.CommandResult, error) {
	if cmd.Stdout == nil {
		return slashcmd.CommandResult{Output: "请在终端交互模式运行 /wecom setup。" + wecomSetupUsage}, nil
	}
	parent := cmd.Ctx
	if parent == nil {
		parent = context.Background()
	}
	// tea.Exec has restored cooked terminal mode: Ctrl+C is now SIGINT.
	// Scope it to this wizard so cancelling returns to the conversation.
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	defer stop()
	cancelled := func() (slashcmd.CommandResult, error) {
		return slashcmd.CommandResult{Output: "企业微信连接已取消，未保存凭据。"}, nil
	}

	fmt.Fprintln(cmd.Stdout, "\n正在连接企业微信…（Ctrl+C 取消）")
	session, err := services.create(ctx)
	if ctx.Err() != nil {
		return cancelled()
	}
	if err != nil {
		return slashcmd.CommandResult{}, fmt.Errorf("初始化企业微信扫码：%w", err)
	}

	fmt.Fprintln(cmd.Stdout, "\n授权链接（可复制到其他设备）：\n"+session.AuthURL)
	fmt.Fprintln(cmd.Stdout, "\n浏览器打开（无摄像头设备可用）：\n"+session.PageURL)
	if err := renderWecomQR(cmd.Stdout, session.AuthURL); err != nil {
		return slashcmd.CommandResult{}, err
	}
	fmt.Fprintln(cmd.Stdout, "请用企业微信 App 扫描上方二维码并确认接入。")
	fmt.Fprintln(cmd.Stdout, "\n等待扫码确认…（最长 5 分钟，Ctrl+C 取消）")
	bot, err := services.wait(ctx, session)
	if ctx.Err() != nil {
		return cancelled()
	}
	if err != nil {
		return slashcmd.CommandResult{}, fmt.Errorf("企业微信扫码未完成（可重新运行 /wecom setup）：%w", err)
	}
	if err := services.save(wecom.Credentials{BotID: bot.BotID, BotSecret: bot.Secret}); err != nil {
		return slashcmd.CommandResult{}, fmt.Errorf("保存企业微信凭据：%w", err)
	}
	return slashcmd.CommandResult{Output: fmt.Sprintf("企业微信连接成功！\nBot ID: %s\n凭据已保存至 %s/wecom-credentials.json", bot.BotID, config.HomeDir())}, nil
}

// renderWecomQR draws the terminal QR with half-block glyphs, independent of
// the terminal theme. Bitmap includes the quiet zone; never wrap its rows.
func renderWecomQR(out io.Writer, link string) error {
	code, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("生成企业微信二维码：%w", err)
	}
	bits := code.Bitmap()
	fmt.Fprintln(out)
	for y := 0; y < len(bits); y += 2 {
		fmt.Fprint(out, "\x1b[30;47m")
		for x := range bits[y] {
			top, bottom := bits[y][x], y+1 < len(bits) && bits[y+1][x]
			glyph := ' '
			switch {
			case top && bottom:
				glyph = '█'
			case top:
				glyph = '▀'
			case bottom:
				glyph = '▄'
			}
			fmt.Fprint(out, string(glyph))
		}
		fmt.Fprint(out, "\x1b[0m\n")
	}
	return nil
}
