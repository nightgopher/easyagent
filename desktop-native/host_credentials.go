package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var errHostCredential = errors.New("主机访问令牌缺失或无法读取，请重新填写访问令牌并保存连接")

func hostCredential(store secretStore, host hostConfig) (string, error) {
	if !host.HasToken {
		return "", nil
	}
	token, err := store.Get("host/" + host.ID)
	if err != nil {
		return "", fmt.Errorf("%w：%v", errHostCredential, err)
	}
	if strings.TrimSpace(token) == "" {
		return "", errHostCredential
	}
	return strings.TrimSpace(token), nil
}

func verifiedHostCredential(store secretStore, id, token string) error {
	if err := store.Set("host/"+id, token); err != nil {
		return err
	}
	read, err := store.Get("host/" + id)
	if err != nil || read != token {
		return fmt.Errorf("%w：保存后回读校验失败，连接未应用", errHostCredential)
	}
	return nil
}

type savedHostProfile struct {
	Config      clientConfig
	Index       int
	NewIdentity bool
}

func persistHostProfile(dir string, store secretStore, config clientConfig, form hostForm) (savedHostProfile, error) {
	result := savedHostProfile{Config: cloneConfig(config), Index: -1}
	b, err := newBackendClient(form.HostURL, strings.TrimSpace(form.HostToken))
	if err != nil {
		return result, err
	}
	for i, h := range config.Hosts {
		if h.ID == form.EditHost && !h.Local {
			result.Index = i
		}
	}
	var previous hostConfig
	if result.Index >= 0 {
		previous = config.Hosts[result.Index]
	}
	oldToken, readErr := hostCredential(store, previous)
	key := b.Token
	if key == "" && previous.HasToken {
		if previous.URL != b.URL {
			return result, errors.New("修改服务地址时请重新填写访问令牌；留空仅保留当前地址的令牌")
		}
		if readErr != nil {
			return result, readErr
		}
	}
	// An explicit repair replaces the unavailable/expired token on this endpoint
	// without stranding its sessions and drafts behind a duplicate host profile.
	// Ordinary edits to the endpoint or identity still create an isolated profile.
	repairing := form.RepairCredential && previous.HasToken && previous.URL == b.URL && key != ""
	result.NewIdentity = !repairing && previous.URL != "" && (previous.URL != b.URL || (key != "" && (readErr != nil || key != oldToken)))
	host := previous
	if result.Index < 0 || result.NewIdentity {
		host = hostConfig{ID: requestID()}
		result.Index = len(result.Config.Hosts)
		result.Config.Hosts = append(result.Config.Hosts, host)
	}
	host.Name, host.URL = strings.TrimSpace(form.HostName), b.URL
	freshCredential := key != "" && host.ID != previous.ID
	if freshCredential || repairing {
		if err = verifiedHostCredential(store, host.ID, key); err != nil {
			if freshCredential {
				err = errors.Join(err, store.Delete("host/"+host.ID))
			} else if repairing && readErr == nil {
				err = errors.Join(err, store.Set("host/"+host.ID, oldToken))
			}
			return result, err
		}
		host.HasToken = true
	}
	result.Config.Hosts[result.Index] = host
	if err = atomicJSON(filepath.Join(dir, "connections.json"), result.Config); err != nil {
		if freshCredential {
			err = errors.Join(err, store.Delete("host/"+host.ID))
		} else if repairing && readErr == nil {
			err = errors.Join(err, store.Set("host/"+host.ID, oldToken))
		}
		return result, err
	}
	return result, nil
}

type removedHostProfile struct {
	Config       clientConfig
	CleanupError error
}

func persistHostRemoval(dir string, store secretStore, config clientConfig, index int) (removedHostProfile, error) {
	next := cloneConfig(config)
	host := next.Hosts[index]
	next.Hosts = append(next.Hosts[:index], next.Hosts[index+1:]...)
	if next.ActiveHost == host.ID {
		next.ActiveHost = "local"
	}
	// Commit removal before credential cleanup: inaccessible credentials must
	// neither block removal nor be lost when saving the profile fails.
	if err := atomicJSON(filepath.Join(dir, "connections.json"), next); err != nil {
		return removedHostProfile{Config: config}, err
	}
	result := removedHostProfile{Config: next}
	if host.HasToken {
		result.CleanupError = store.Delete("host/" + host.ID)
	}
	return result, nil
}

func (a *preview) reportLiveError(err error) {
	l := a.Live
	if (errors.Is(err, errHostCredential) || errors.Is(err, errUnauthorized)) && !a.Settings.host().Local {
		l.CredentialRepair = true
		l.Connected, l.Connecting = false, false
		l.Error = err.Error()
		if l.streamCancel != nil {
			l.streamCancel()
		}
	} else if !l.CredentialRepair {
		l.Error = err.Error()
	}
}

func (a *preview) repairLiveCredential() {
	l := a.Live
	if a.Settings.host().Local {
		return
	}
	if a.Settings.Open && a.settingsFormDirty() {
		l.Confirm = &nativeConfirmation{"放弃未保存的修改？", "重新填写连接令牌前，将放弃当前表单的未保存修改。", func() { a.discardSettingsForms(); a.repairLiveCredential() }}
		return
	}
	host := l.Config.Hosts[a.Settings.Host]
	a.openSettings("connections")
	a.Settings.hostForm = hostForm{EditHost: host.ID, HostName: host.Name, HostURL: host.URL, RepairCredential: true}
	a.Settings.Heading.CancelFocus()
	a.Settings.HostTokenField.Focus()
}
