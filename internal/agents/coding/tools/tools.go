package tools

import (
	"github.com/nightgopher/easyagent/internal/computer"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/operations"
	basetools "github.com/nightgopher/easyagent/sdk/tools"
)

// ListOptions controls how the coding-agent toolset is assembled.
type ListOptions struct {
	AllowOutsideWorkspace  bool
	Workspace              string
	MaxOutputLen           int
	EnableBash             bool
	BashOps                operations.BashOperations
	EnableWeb              bool
	WebTimeoutSeconds      int
	EnableWebSearch        bool
	EnableComputerUse      bool // experimental: expose computer tool
	ComputerDataDir        string
	ComputerSessionID      string
	ComputerApprovalPolicy string // "ask" | "auto"
	ComputerApprovedApps   []string
	FileOps                operations.FileOperations
	ExtensionTools         []agent.Tool
	AllowedTools           []string
	BlockedTools           []string
	FileMutationQueue      *FileMutationQueue         // 可选：per-file 写操作串行化
	BackupManager          *basetools.BackupManager   // 可选：操作前自动快照
	ReadTracker            *basetools.ReadTracker     // 可选：读后外部修改保护
	BackgroundTasks        *basetools.BackgroundTasks // 可选：bash 后台任务，提供时注册 bash_output / bash_kill
}

// BaseToolNames returns the canonical coding-agent tool names before extension tools.
func BaseToolNames(enableBash bool) []string {
	names := []string{"read", "write", "edit", "grep", "find", "ls", "patch", "todo_write", "save_memory", "local_time", "delete_file", "ask_user_question"}
	if enableBash {
		names = append([]string{"bash", "bash_output", "bash_kill"}, names...)
	}
	return names
}

// WebSearchToolNames returns the canonical web tool names.
func WebSearchToolNames() []string {
	return []string{"web_search"}
}

// BuildList assembles the concrete coding-agent toolset.
func BuildList(opts ListOptions) []agent.Tool {
	toolList := []agent.Tool{}

	if opts.EnableBash {
		toolList = append(toolList, basetools.NewBashTool(
			basetools.WithBashWorkspace(opts.Workspace),
			basetools.WithBashMaxOutputLen(opts.MaxOutputLen),
			basetools.WithBashOperations(opts.BashOps),
			basetools.WithBashBackground(opts.BackgroundTasks),
		))
		if opts.BackgroundTasks != nil {
			toolList = append(toolList,
				basetools.NewBashOutputTool(opts.BackgroundTasks, opts.MaxOutputLen),
				basetools.NewBashKillTool(opts.BackgroundTasks),
			)
		}
	}

	if opts.EnableWeb {
		toolList = append(toolList, basetools.NewWebFetchTool(
			basetools.WithWebFetchTimeout(opts.WebTimeoutSeconds),
			basetools.WithWebFetchMaxOutputLen(opts.MaxOutputLen),
		))
	}

	if opts.EnableComputerUse {
		tool := computer.NewTool(opts.ComputerDataDir)
		tool.ConfigureSession(opts.ComputerSessionID)
		tool.ConfigurePolicy(opts.ComputerApprovalPolicy, opts.ComputerApprovedApps)
		toolList = append(toolList, tool)
	}

	if opts.EnableWebSearch {
		toolList = append(toolList, basetools.NewWebSearchTool())
	}

	toolList = append(toolList,
		basetools.NewReadTool(
			basetools.WithReadWorkspace(opts.Workspace),
			basetools.WithReadPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithReadMaxOutputLen(opts.MaxOutputLen),
			basetools.WithReadOperations(opts.FileOps),
			basetools.WithReadTracker(opts.ReadTracker),
		),
		basetools.NewWriteTool(
			basetools.WithWriteWorkspace(opts.Workspace),
			basetools.WithWritePathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithWriteOperations(opts.FileOps),
			basetools.WithWriteMutationQueue(opts.FileMutationQueue),
			basetools.WithWriteBackupManager(opts.BackupManager),
			basetools.WithWriteReadTracker(opts.ReadTracker),
		),
		basetools.NewEditTool(
			basetools.WithEditWorkspace(opts.Workspace),
			basetools.WithEditPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithEditOperations(opts.FileOps),
			basetools.WithEditMutationQueue(opts.FileMutationQueue),
			basetools.WithEditBackupManager(opts.BackupManager),
			basetools.WithEditReadTracker(opts.ReadTracker),
		),
		basetools.NewGrepTool(
			basetools.WithGrepWorkspace(opts.Workspace),
			basetools.WithGrepPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithGrepMaxOutputLen(opts.MaxOutputLen),
			basetools.WithGrepOperations(opts.FileOps),
		),
		basetools.NewFindTool(
			basetools.WithFindWorkspace(opts.Workspace),
			basetools.WithFindPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithFindOperations(opts.FileOps),
		),
		basetools.NewLsTool(
			basetools.WithLsWorkspace(opts.Workspace),
			basetools.WithLsPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithLsMaxOutputLen(opts.MaxOutputLen),
			basetools.WithLsOperations(opts.FileOps),
		),
		basetools.NewPatchTool(
			basetools.WithPatchWorkspace(opts.Workspace),
			basetools.WithPatchPathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithPatchOperations(opts.FileOps),
			basetools.WithPatchBackupManager(opts.BackupManager),
		),
		basetools.NewTodoTool(),
		basetools.NewMemoryTool(),
		basetools.NewLocalTimeTool(),
		basetools.NewDeleteFileTool(
			basetools.WithDeleteFileWorkspace(opts.Workspace),
			basetools.WithDeleteFilePathPolicy(basetools.PathPolicy{AllowOutsideWorkspace: opts.AllowOutsideWorkspace}),
			basetools.WithDeleteFileOperations(opts.FileOps),
			basetools.WithDeleteFileBackupManager(opts.BackupManager),
		),
		basetools.NewAskUserTool(),
	)

	toolList = append(toolList, opts.ExtensionTools...)
	return filterTools(toolList, opts.AllowedTools, opts.BlockedTools)
}

func filterTools(tools []agent.Tool, allowed []string, blocked []string) []agent.Tool {
	if len(allowed) == 0 && len(blocked) == 0 {
		return tools
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	blockedSet := make(map[string]bool, len(blocked))
	for _, name := range blocked {
		blockedSet[name] = true
	}

	var filtered []agent.Tool
	for _, tool := range tools {
		name := tool.Name()
		if len(allowed) > 0 && !allowedSet[name] {
			continue
		}
		if blockedSet[name] {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}
