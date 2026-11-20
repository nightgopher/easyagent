//go:build android || ios

package main

// 手机端没有 libghostty-vt 原生库，plugins/terminal 无法构建：终端面板在
// 移动版暂不可用，其余能力（会话、对话、审批）不受影响。
const terminalUnsupported = "手机版暂不支持终端面板，请使用桌面端或远程主机的其他终端"

// newTerminal is the mobile stub: the terminal panel needs plugins/terminal.
func (a *preview) newTerminal() *terminalTab {
	a.Live.Error = terminalUnsupported
	return nil
}
