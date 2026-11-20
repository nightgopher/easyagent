//go:build android || ios

package main

// phoneClient marks builds where exec-based features (本地核心、CLI 助手)
// are unavailable and the app acts as a pure remote client.
const phoneClient = true
