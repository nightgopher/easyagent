package commands

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/internal/wecom"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

const wecomTestAuthURL = "https://work.weixin.qq.com/ai/qc/gen?u=test-only"

func wecomTestServices(saved *int) wecomSetupServices {
	return wecomSetupServices{
		create: func(context.Context) (*wecom.QrSession, error) {
			return &wecom.QrSession{Scode: "sc-test", AuthURL: wecomTestAuthURL, PageURL: "https://work.weixin.qq.com/ai/qc/gen?scode=sc-test"}, nil
		},
		wait: func(context.Context, *wecom.QrSession) (wecom.Bot, error) {
			return wecom.Bot{BotID: "bot-test", Secret: "wecom-test-secret"}, nil
		},
		save: func(c wecom.Credentials) error {
			*saved++
			if c.BotSecret != "wecom-test-secret" {
				return errors.New("secret lost")
			}
			return nil
		},
	}
}

func TestWecomSetupSuccess(t *testing.T) {
	saved := 0
	var out bytes.Buffer
	result, err := runWecomSetup(slashcmd.Context{Ctx: t.Context(), Stdout: &out}, wecomTestServices(&saved))
	if err != nil || saved != 1 {
		t.Fatalf("result=%+v err=%v saved=%d", result, err, saved)
	}
	for _, want := range []string{wecomTestAuthURL, "scode=sc-test", "\x1b[30;47m", "bot-test", "wecom-credentials.json"} {
		if !strings.Contains(out.String()+result.Output, want) {
			t.Fatalf("output missing %q", want)
		}
	}
	if strings.Contains(result.Output, "wecom-test-secret") {
		t.Fatal("secret exposed in transcript")
	}
}

func TestWecomSetupCancelDuringWaitKeepsCredentials(t *testing.T) {
	saved := 0
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	services := wecomTestServices(&saved)
	origWait := services.wait
	services.wait = func(ctx context.Context, s *wecom.QrSession) (wecom.Bot, error) {
		cancel()
		return origWait(ctx, s)
	}
	result, err := runWecomSetup(slashcmd.Context{Ctx: ctx, Stdout: &out}, services)
	if err != nil {
		t.Fatalf("cancelled setup must not fail: %v", err)
	}
	if !strings.Contains(result.Output, "已取消") {
		t.Fatalf("unexpected output: %s", result.Output)
	}
	if saved != 0 {
		t.Fatalf("credentials saved on cancelled setup: %d", saved)
	}
}

func TestWecomSetupCreateError(t *testing.T) {
	saved := 0
	var out bytes.Buffer
	services := wecomSetupServices{
		create: func(context.Context) (*wecom.QrSession, error) { return nil, errors.New("network down") },
		wait:   func(context.Context, *wecom.QrSession) (wecom.Bot, error) { return wecom.Bot{}, nil },
		save:   func(wecom.Credentials) error { return nil },
	}
	if _, err := runWecomSetup(slashcmd.Context{Ctx: t.Context(), Stdout: &out}, services); err == nil || !strings.Contains(err.Error(), "初始化企业微信扫码") {
		t.Fatalf("want create error, got %v", err)
	}
	if saved != 0 {
		t.Fatalf("credentials saved on failed setup: %d", saved)
	}
}

func TestWecomSetupRequiresTerminal(t *testing.T) {
	saved := 0
	result, err := runWecomSetup(slashcmd.Context{Ctx: t.Context()}, wecomTestServices(&saved))
	if err != nil || !strings.Contains(result.Output, "终端交互模式") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if saved != 0 {
		t.Fatalf("credentials saved without terminal: %d", saved)
	}
}

func TestWecomStatusAndLogout(t *testing.T) {
	t.Setenv("EA_HOME", t.TempDir())
	result, err := handleWecomStatus()
	if err != nil || !strings.Contains(result.Output, "尚未连接") {
		t.Fatalf("status=%+v err=%v", result, err)
	}
	if err := wecom.SaveCredentials(wecom.Credentials{BotID: "bot-status", BotSecret: "sec"}); err != nil {
		t.Fatal(err)
	}
	result, err = handleWecomStatus()
	if err != nil || !strings.Contains(result.Output, "bot-status") {
		t.Fatalf("status=%+v err=%v", result, err)
	}
	if result, err = handleWecomLogout(); err != nil || !strings.Contains(result.Output, "已清除") {
		t.Fatalf("logout=%+v err=%v", result, err)
	}
	if result, err = handleWecomStatus(); err != nil || !strings.Contains(result.Output, "尚未连接") {
		t.Fatalf("status after logout=%+v err=%v", result, err)
	}
}
