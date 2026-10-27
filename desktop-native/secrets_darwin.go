//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/nightgopher/easyagent/desktop-native/internal/credentialstore"
)

type systemSecrets struct {
	dir, helper, path string // helper/path are used only by isolated integration tests.
}

var credentialMu sync.Mutex
var cachedCredentialName = regexp.MustCompile(`^credentials-[a-f0-9]{40}$`)

// Also used by the microphone bridge for pointers returned by Apple APIs.
func nativePointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}

func credentialRepairError() error {
	return errors.New("系统钥匙串无法自动读取已保存凭据，请在设置中重新填写访问令牌或模型 API 密钥；此处不需要 Mac 登录密码")
}

// The non-secret owner index selects the latest writer when more than one
// helper generation is cached. Directory/hash order cannot establish freshness.
// A file lock serializes updates across two simultaneously running app versions.
func (s systemSecrets) begin() ([]string, map[string]string, func(), error) {
	dir := filepath.Join(s.dir, "credential-helpers.noindex")
	if s.dir == "" {
		return nil, nil, nil, errors.New("无法定位凭据程序的应用数据目录")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, nil, nil, errors.New("凭据程序目录必须仅当前用户可访问")
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, nil, nil, err
	}
	done := func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }
	paths, err := s.helperPaths()
	if err != nil {
		done()
		return nil, nil, nil, err
	}
	owners := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "owners.json"))
	if os.IsNotExist(err) {
		err = nil
	} else if err == nil && json.Unmarshal(data, &owners) != nil {
		err = errors.New("凭据程序索引损坏，请重新填写凭据")
	}
	if err != nil || owners == nil {
		done()
		return nil, nil, nil, errors.New("无法读取凭据程序索引")
	}
	return paths, owners, done, nil
}

func (s systemSecrets) recordOwner(owners map[string]string, account, helper string) error {
	if owners[account] == filepath.Base(helper) {
		return nil
	}
	owners[account] = filepath.Base(helper)
	return atomicJSON(filepath.Join(s.dir, "credential-helpers.noindex/owners.json"), owners)
}

// Verify every executable against the app's certificate before sending a secret.
func (s systemSecrets) helperPaths() ([]string, error) {
	requirement, _, err := credentialstore.Identity("")
	identifier := `identifier "` + credentialstore.AppID + `"`
	if err != nil || !strings.Contains(requirement, identifier) || strings.Contains(requirement, "cdhash") {
		return nil, errors.New("安全凭据保存需要固定证书签名的 EasyAgent")
	}
	requirement = strings.Replace(requirement, identifier, `identifier "`+credentialstore.HelperID+`"`, 1)
	source := s.helper
	if source == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		source = filepath.Join(filepath.Dir(executable), "../Resources/easyagent-credentials")
	}
	if err := credentialstore.Verify(source, 0, requirement); err != nil {
		return nil, err
	}
	_, hash, err := credentialstore.Identity(source)
	if err != nil {
		return nil, err
	}
	if s.dir == "" {
		return nil, errors.New("无法定位凭据程序的应用数据目录")
	}
	dir := filepath.Join(s.dir, "credential-helpers.noindex")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("凭据程序目录必须仅当前用户可访问")
	}
	current := filepath.Join(dir, "credentials-"+hash)
	if _, err := os.Lstat(current); os.IsNotExist(err) {
		input, err := os.Open(source)
		if err != nil {
			return nil, err
		}
		defer input.Close()
		output, err := os.CreateTemp(dir, ".credentials-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(output.Name())
		_, err = io.Copy(output, input)
		if err == nil {
			err = output.Chmod(0500)
		}
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = credentialstore.Verify(output.Name(), 0, requirement)
		}
		if err == nil {
			err = os.Rename(output.Name(), current)
		}
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := []string{current}
	for _, entry := range entries {
		if cachedCredentialName.MatchString(entry.Name()) && entry.Name() != filepath.Base(current) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	verified := paths[:0]
	for _, path := range paths {
		if err := verifyCachedCredentialHelper(path, requirement); err != nil {
			if path == current {
				return nil, err
			}
			// Old signing identities cannot authenticate this app. Retain their
			// files and owner entries, but never execute them or let them block
			// saving replacement credentials with the current trusted helper.
			continue
		}
		verified = append(verified, path)
	}
	return verified, nil
}

func verifyCachedCredentialHelper(path, requirement string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("缓存凭据程序的文件权限或类型无效")
	}
	if err := credentialstore.Verify(path, 0, requirement); err != nil {
		return err
	}
	_, actual, err := credentialstore.Identity(path)
	if err != nil || filepath.Base(path) != "credentials-"+actual {
		return errors.New("缓存凭据程序的代码哈希不匹配")
	}
	return nil
}

func (s systemSecrets) request(helper, operation, account, value string) (credentialstore.Response, error) {
	data, err := json.Marshal(credentialstore.Request{Operation: operation, Account: account, Value: value, Keychain: s.path})
	if err != nil {
		return credentialstore.Response{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper)
	input, err := cmd.StdinPipe()
	if err != nil {
		return credentialstore.Response{}, err
	}
	outputPipe, err := cmd.StdoutPipe()
	if err != nil {
		return credentialstore.Response{}, err
	}
	if err := cmd.Start(); err != nil {
		return credentialstore.Response{}, errors.New("无法启动安全凭据程序")
	}
	// Authenticate the running child before putting a secret on its stdin.
	// Static verification alone leaves a verify/exec replacement race.
	requirement, _, identityErr := credentialstore.Identity("")
	requirement = strings.Replace(requirement, `identifier "`+credentialstore.AppID+`"`, `identifier "`+credentialstore.HelperID+`"`, 1)
	if identityErr != nil || credentialstore.Verify("", cmd.Process.Pid, requirement) != nil {
		_ = input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return credentialstore.Response{}, errors.New("凭据程序的运行身份校验失败")
	}
	_, writeErr := io.Copy(input, bytes.NewReader(data))
	_ = input.Close()
	output, readErr := io.ReadAll(io.LimitReader(outputPipe, (100<<10)+1))
	err = cmd.Wait()
	if err != nil || writeErr != nil || readErr != nil || len(output) > 100<<10 {
		return credentialstore.Response{}, errors.New("安全凭据程序未能完成请求")
	}
	var response credentialstore.Response
	if json.Unmarshal(output, &response) != nil {
		return response, errors.New("安全凭据程序响应无效")
	}
	if response.Error != "" {
		return credentialstore.Response{}, errors.New(response.Error)
	}
	return response, nil
}

func (s systemSecrets) Get(account string) (string, error) {
	credentialMu.Lock()
	defer credentialMu.Unlock()
	paths, owners, done, err := s.begin()
	if err != nil {
		return "", err
	}
	defer done()
	if owner := owners[account]; owner != "" {
		path := ""
		for _, candidate := range paths {
			if filepath.Base(candidate) == owner {
				path = candidate
			}
		}
		if path == "" {
			return "", credentialRepairError()
		}
		response, err := s.request(path, "get", account, "")
		if err != nil || !response.Found {
			return "", credentialRepairError()
		}
		if path != paths[0] {
			if err := s.save(paths[0], account, response.Value); err != nil {
				return "", err
			}
			if err := s.recordOwner(owners, account, paths[0]); err != nil {
				return "", err
			}
		}
		return response.Value, nil
	}
	// A protected old item remains intact. A replacement can be saved without
	// asking for an unrelated keychain password or weakening its access rules.
	if err := credentialstore.DisableInteraction(); err != nil {
		return "", err
	}
	legacy := credentialstore.Store{Path: s.path, Service: credentialstore.LegacyService}
	value, found, err := legacy.Get(account)
	if err != nil {
		return "", credentialRepairError()
	}
	if found {
		if err := s.save(paths[0], account, value); err != nil {
			return "", err
		}
		if err := s.recordOwner(owners, account, paths[0]); err != nil {
			return "", err
		}
	}
	return value, nil
}

func (s systemSecrets) save(helper, account, value string) error {
	if _, err := s.request(helper, "set", account, value); err != nil {
		return err
	}
	read, err := s.request(helper, "get", account, "")
	if err != nil || !read.Found || read.Value != value {
		return errors.New("凭据保存后回读校验失败，配置未应用")
	}
	return nil
}

func (s systemSecrets) Set(account, value string) error {
	if value == "" {
		return s.Delete(account)
	}
	credentialMu.Lock()
	defer credentialMu.Unlock()
	paths, owners, done, err := s.begin()
	if err != nil {
		return err
	}
	defer done()
	if err := s.save(paths[0], account, value); err != nil {
		return err
	}
	return s.recordOwner(owners, account, paths[0])
}

func (s systemSecrets) Delete(account string) error {
	credentialMu.Lock()
	defer credentialMu.Unlock()
	paths, owners, done, err := s.begin()
	if err != nil {
		return err
	}
	defer done()
	// The empty successor prevents cleared legacy values from reappearing.
	if err := s.save(paths[0], account, ""); err != nil {
		return err
	}
	if err := s.recordOwner(owners, account, paths[0]); err != nil {
		return err
	}
	for _, path := range paths[1:] {
		_, _ = s.request(path, "delete", account, "")
	}
	if err := credentialstore.DisableInteraction(); err != nil {
		return err
	}
	_ = (credentialstore.Store{Path: s.path, Service: credentialstore.LegacyService}).Delete(account)
	return nil
}
