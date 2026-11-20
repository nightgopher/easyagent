//go:build !darwin

package main

import (
	"github.com/egoist/mygo"
)

// systemSecrets 用 mygo.Secrets 保存凭据：macOS 之外平台的系统级凭据存储
// （Android 为应用存储并用系统密钥库加密，iOS 钥匙串，Linux/Windows 为
// Secret Service / Credential Manager）。没有凭据存储的环境（无 Secret
// Service 的服务器等）返回 errors.ErrUnsupported，由调用方按凭据错误处理。
//
// dir 保留是为了与 darwin 版构造处兼容（mygo.Secrets 自行定位存储）。
type systemSecrets struct{ dir string }

func (systemSecrets) Get(key string) (string, error) {
	value, err := mygo.Secrets.Get(key)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func (systemSecrets) Set(key, value string) error {
	return mygo.Secrets.Set(key, []byte(value))
}

func (systemSecrets) Delete(key string) error {
	return mygo.Secrets.Delete(key)
}
