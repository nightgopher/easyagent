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
	if form.EditHost != "" && result.Index < 0 {
		return result, errors.New("正在编辑的连接已不存在，请重新选择连接")
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
	// A changed endpoint or credential needs its own storage identity so drafts
	// from a different server/account cannot be sent to it.
	repairing := form.RepairCredential && previous.HasToken && previous.URL == b.URL && key != ""
	result.NewIdentity = !repairing && previous.URL != "" && (previous.URL != b.URL || (key != "" && (readErr != nil || key != oldToken)))
	if previous.Relay != nil && result.NewIdentity {
		return result, errors.New("配对主机的地址或身份不能修改，请重新配对")
	}
	host := previous
	if result.Index < 0 {
		host = hostConfig{ID: requestID()}
		result.Index = len(result.Config.Hosts)
		result.Config.Hosts = append(result.Config.Hosts, host)
	} else if result.NewIdentity {
		// Preserve prior identities and their credentials off-list. If the user
		// later returns to the same endpoint and credential, recover its drafts.
		group := previous.HistoryGroup
		if group == "" {
			group = previous.ID
		}
		host = hostConfig{ID: requestID(), HistoryGroup: group}
		for i, candidate := range result.Config.HostHistory {
			candidateGroup := candidate.HistoryGroup
			if candidateGroup == "" {
				candidateGroup = candidate.ID
			}
			if candidateGroup != group || candidate.URL != b.URL {
				continue
			}
			stored, err := hostCredential(store, candidate)
			if err == nil && stored == key {
				host = candidate
				result.Config.HostHistory = append(result.Config.HostHistory[:i], result.Config.HostHistory[i+1:]...)
				break
			}
		}
		result.Config.HostHistory = append(result.Config.HostHistory, previous)
		if config.ActiveHost == previous.ID {
			result.Config.ActiveHost = host.ID
		}
	}
	host.Name, host.URL = strings.TrimSpace(form.HostName), b.URL
	freshCredential := key != "" && host.ID != previous.ID && !host.HasToken
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
	group := host.HistoryGroup
	if group == "" {
		group = host.ID
	}
	var history, retained []hostConfig
	for _, candidate := range next.HostHistory {
		candidateGroup := candidate.HistoryGroup
		if candidateGroup == "" {
			candidateGroup = candidate.ID
		}
		if candidateGroup == group {
			history = append(history, candidate)
		} else {
			retained = append(retained, candidate)
		}
	}
	next.HostHistory = retained
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
	for _, candidate := range history {
		if candidate.HasToken {
			result.CleanupError = errors.Join(result.CleanupError, store.Delete("host/"+candidate.ID))
		}
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
