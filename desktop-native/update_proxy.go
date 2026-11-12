package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
)

// The updater's proxy preference. mygo's updater talks through netproxy
// (system proxy with env-var override), which breaks when another tool on
// the machine rewrites the system proxy settings. This preference pins
// update traffic to direct or a fixed proxy URL, applied through
// mygo.Updater.SetSource so the manifest check and the package download
// both follow it. 跟随系统 (the default) restores mygo's own feed source.

const (
	updateProxySystem  = "跟随系统"
	updateProxyDirect  = "直连"
	updateProxyCustom  = "自定义代理"
	updateProxyDefault = updateProxySystem
)

// updateFeedBase mirrors mygo.json's updates.github; that file stays the
// source of truth. A var so tests can point it at a stub server.
var updateFeedBase = "https://github.com/nightgopher/easyagent/releases/latest/download"

func updateFeedURL() string {
	return updateFeedBase + "/update-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
}

// updaterTransport returns the HTTP transport for the preference: netproxy
// as-is for 跟随系统, proxy-free for 直连, and a fixed proxy URL for
// 自定义代理.
func updaterTransport(mode, rawURL string) (*http.Transport, error) {
	t := netproxy.Transport().Clone()
	switch mode {
	case updateProxyDirect:
		t.Proxy = nil
	case updateProxyCustom:
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid update proxy URL: %s", rawURL)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("invalid update proxy URL: %s", rawURL)
		}
		proxy := u
		t.Proxy = func(*http.Request) (*url.URL, error) { return proxy, nil }
	default:
		return nil, fmt.Errorf("unknown update proxy mode: %s", mode)
	}
	return t, nil
}

// proxyGet mirrors mygo's httpGet through the transport of the proxy
// preference: HTTPS only outside loopback, a fixed ASCII user agent (a
// byte outside ASCII in the agent makes GitHub's release store answer 400
// to every file its CDN has not cached yet), and errors for non-200.
func proxyGet(t *http.Transport) func(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	client := &http.Client{Transport: t}
	return func(ctx context.Context, rawURL string) (io.ReadCloser, error) {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
			return nil, fmt.Errorf("refusing to download %s: updates need HTTPS", rawURL)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "mygo-updater")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
		}
		return resp.Body, nil
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// proxyUpdateSource serves mygo build's update feed through the transport
// of the proxy preference. This app never enables prereleases, so the
// latest stable feed answers that flag too.
type proxyUpdateSource struct {
	get func(ctx context.Context, url string) (io.ReadCloser, error)
}

func (s proxyUpdateSource) Manifest(ctx context.Context, prereleases bool) (io.ReadCloser, error) {
	return s.get(ctx, updateFeedURL())
}

func (s proxyUpdateSource) Open(ctx context.Context, url string) (io.ReadCloser, error) {
	return s.get(ctx, url)
}

// applyUpdateProxy wires the saved proxy preference into mygo's updater.
// An unusable custom proxy falls back to the system setting rather than
// disabling update checks. Safe to call again after a preference change.
func (a *preview) applyUpdateProxy() {
	mode := a.Settings.Preferences.UpdateProxyMode
	if mode == "" || mode == updateProxySystem {
		mygo.Updater.SetSource(nil)
		return
	}
	t, err := updaterTransport(mode, a.Settings.Preferences.UpdateProxyURL)
	if err != nil {
		mygo.Updater.SetSource(nil)
		return
	}
	mygo.Updater.SetSource(proxyUpdateSource{get: proxyGet(t)})
}
