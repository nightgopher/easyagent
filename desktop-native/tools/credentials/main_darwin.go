// The parent application is updated normally; this small signed executable is
// cached by code hash so macOS sees the same keychain client across app updates.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/hwj123hwj/easyagent/desktop-native/internal/credentialstore"
)

type savedCredential struct {
	Version int
	Value   string
}

func serve() credentialstore.Response {
	if err := credentialstore.DisableInteraction(); err != nil {
		return credentialstore.Response{Error: err.Error()}
	}
	requirement, hash, err := credentialstore.Identity("")
	if err != nil {
		return credentialstore.Response{Error: err.Error()}
	}
	identifier := `identifier "` + credentialstore.HelperID + `"`
	if !strings.Contains(requirement, identifier) || strings.Contains(requirement, "cdhash") {
		return credentialstore.Response{Error: "凭据程序需要固定证书签名"}
	}
	parentRequirement := strings.Replace(requirement, identifier, `identifier "`+credentialstore.AppID+`"`, 1)
	parent := os.Getppid()
	if parent <= 1 || credentialstore.Verify("", parent, parentRequirement) != nil || os.Getppid() != parent {
		return credentialstore.Response{Error: "凭据访问被拒绝：调用方不是同一签名的 EasyAgent"}
	}
	var request credentialstore.Request
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 100<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return credentialstore.Response{Error: "凭据请求无效"}
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || request.Account == "" || len(request.Account) > 512 || strings.ContainsRune(request.Account, 0) || len(request.Value) > 65536 {
		return credentialstore.Response{Error: "凭据请求无效"}
	}
	store := credentialstore.Store{Path: request.Keychain, Service: credentialstore.Service + hash}
	var response credentialstore.Response
	switch request.Operation {
	case "get":
		response.Value, response.Found, err = store.Get(request.Account)
		if err == nil && response.Found {
			var saved savedCredential
			if json.Unmarshal([]byte(response.Value), &saved) != nil || saved.Version != 1 {
				err = errors.New("保存的凭据格式无效")
			} else {
				response.Value = saved.Value
			}
		}
	case "set", "delete":
		_, _, err = store.Get(request.Account)
		if err == nil {
			if request.Operation == "delete" {
				request.Value = ""
			}
			data, encodeErr := json.Marshal(savedCredential{Version: 1, Value: request.Value})
			err = encodeErr
			if err == nil {
				err = store.Set(request.Account, string(data))
			}
		}
	default:
		err = errors.New("不支持的凭据操作")
	}
	if err != nil {
		return credentialstore.Response{Error: err.Error()}
	}
	return response
}

func main() {
	// The authenticated parent owns the pipe. No secret is read from arguments,
	// inherited environment variables, files or a listening socket.
	_ = json.NewEncoder(os.Stdout).Encode(serve())
}
