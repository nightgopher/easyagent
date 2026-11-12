package agent

import "errors"

// ErrAgentBusy 当 Agent 正在处理请求时，再次调用 Prompt / PromptStream 会返回此错误。
var ErrAgentBusy = errors.New("agent is busy processing another request")

// ErrAgentNotRunning 当 Agent 空闲（没有正在处理的请求）时，调用 Steer 会返回此错误；
// 调用方（如 server / UI）可据此回退为普通 Prompt。
var ErrAgentNotRunning = errors.New("agent is not running")
