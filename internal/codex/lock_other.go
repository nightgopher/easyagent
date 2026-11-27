//go:build !darwin && !linux && !windows

package codex

import (
	"context"
	"errors"
	"os"
)

func openCredential(string) (*os.File, error) {
	return nil, errors.New("当前系统暂不支持安全读取 Codex 凭据")
}
func lockCredentials(context.Context, string) (func(), error) {
	return nil, errors.New("当前系统暂不支持安全保存 Codex 凭据")
}
