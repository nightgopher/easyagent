package coding

import (
	"context"
	"github.com/nightgopher/easyagent/internal/agents/coding/commands"
	codingprofile "github.com/nightgopher/easyagent/internal/agents/coding/profile"
	codingprompt "github.com/nightgopher/easyagent/internal/agents/coding/prompt"
	codingtools "github.com/nightgopher/easyagent/internal/agents/coding/tools"
	"github.com/nightgopher/easyagent/internal/capture"
	modelsreg "github.com/nightgopher/easyagent/internal/models"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
	basetools "github.com/nightgopher/easyagent/sdk/tools"
	"log/slog"
	"time"
)

// CodingApplication implements runtime.Application for the coding-agent.
// It is the concrete application that gets injected into the Platform layer.
type CodingApplication struct {
	Cfg      config.Config
	modelReg *modelsreg.Registry
}

// NewCodingApplication creates a new CodingApplication with the given config.
func NewCodingApplication(cfg config.Config) CodingApplication {
	modelConfigPath := modelsreg.ResolveConfigPath(cfg.DataDir)
	reg := modelsreg.NewDefaultRegistry(modelConfigPath)

	// 网关模式以远端目录为准，本地清单只补充模型元数据。
	// 拉取失败保留本地清单，并明确提示，避免把回退误认为同步成功。
	if cfg.OpenAIBaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if gwModels, err := modelsreg.FetchGatewayModelDefs(ctx, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey); err == nil {
			if cfg.Provider == "openai" {
				gatewayReg := modelsreg.NewRegistry()
				gatewayReg.MergeGatewayModels("openai", gwModels)
				// 本地已有的已知元数据优先覆盖最小推断
				for _, m := range gwModels {
					if localDef, ok := reg.Get(m.ID); ok {
						if localDef.SupportsImages != nil {
							mDef, _ := gatewayReg.Get(m.ID)
							mDef.SupportsImages = localDef.SupportsImages
							gatewayReg.Register(mDef)
						}
						if localDef.ContextWindow > 0 {
							mDef, _ := gatewayReg.Get(m.ID)
							mDef.ContextWindow = localDef.ContextWindow
							gatewayReg.Register(mDef)
						}
					}
				}
				reg = gatewayReg
			} else {
				reg.MergeGatewayModels("openai", gwModels)
			}
		} else {
			slog.Warn("gateway model discovery failed; using local model catalog", "error", err)
		}
		cancel()
	}

	return CodingApplication{
		Cfg:      cfg,
		modelReg: reg,
	}
}

// BuildTools assembles the coding-agent toolset based on the provided options.
func (a CodingApplication) BuildTools(opts runtime.ToolBuildOptions) []agent.Tool {
	mutationQueue := codingtools.NewFileMutationQueue()
	backupMgr := commands.GetUndoManager()

	readTracker := basetools.NewReadTracker()
	var backgroundTasks *basetools.BackgroundTasks
	if ext, ok := opts.SessionExt.(*CodingSessionExt); ok && ext != nil {
		readTracker = ext.ReadTracker()
		backgroundTasks = ext.BackgroundTasks()
	}

	extensions := append([]agent.Tool{}, opts.ExtensionTools...)
	extensions = append(extensions, capture.Tool{Store: capture.New(a.Cfg.DataDir), Source: "session:" + opts.SessionID})
	return codingtools.BuildList(codingtools.ListOptions{
		Workspace:              opts.Workspace,
		AllowOutsideWorkspace:  a.Cfg.AllowOutsideWorkspace,
		MaxOutputLen:           opts.MaxOutputLen,
		EnableBash:             a.Cfg.EnableBash,
		BashOps:                opts.BashOps,
		EnableWeb:              a.Cfg.EnableWeb,
		WebTimeoutSeconds:      a.Cfg.WebTimeoutSeconds,
		EnableWebSearch:        a.Cfg.EnableWebSearch,
		EnableComputerUse:      a.Cfg.EnableComputerUse,
		ComputerDataDir:        a.Cfg.DataDir,
		ComputerSessionID:      opts.SessionID,
		ComputerApprovalPolicy: a.Cfg.ComputerApprovalPolicy,
		ComputerApprovedApps:   a.Cfg.ApprovedApps,
		FileOps:                opts.FileOps,
		ExtensionTools:         extensions,
		AllowedTools:           opts.AllowedTools,
		BlockedTools:           opts.BlockedTools,
		FileMutationQueue:      mutationQueue,
		BackupManager:          backupMgr,
		ReadTracker:            readTracker,
		BackgroundTasks:        backgroundTasks,
	})
}

// BuildPrompt constructs the coding-agent system prompt based on the provided options.
func (a CodingApplication) BuildPrompt(opts runtime.PromptBuildOptions, profileName, goal string) string {
	return codingprompt.BuildSystemPrompt(codingprompt.Options{
		CustomPrompt: opts.CustomPrompt,
		CWD:          opts.CWD,
		Tools:        opts.Tools,
		ContextFiles: opts.ContextFiles,
		Skills:       opts.Skills,
		Profile:      profileName,
		Goal:         goal,
	})
}

// NewSessionExt creates a per-session CodingSessionExt.
// The rebuild callback is nil initially; AgentSession injects it later via SetRebuild.
func (a CodingApplication) NewSessionExt() runtime.SessionExt {
	ext := NewCodingSessionExt(nil)
	ext.background = basetools.NewBackgroundTasks(basetools.WithBackgroundDataDir(a.Cfg.DataDir))
	return ext
}

// Profiles returns the list of available profile names.
func (CodingApplication) Profiles() []string {
	return codingprofile.All()
}

// AvailableModels returns the list of models available for switching.
// Uses the config-driven model registry, falling back to defaults.
func (a CodingApplication) AvailableModels() []slashcmd.ModelInfo {
	all := a.modelReg.List()
	result := make([]slashcmd.ModelInfo, 0, len(all))
	for _, m := range all {
		result = append(result, slashcmd.ModelInfo{
			Provider: m.Provider,
			ModelID:  m.ID,
		})
	}
	return result
}

// ResolveModel applies the same catalog metadata to inference and model listings.
func (a CodingApplication) ResolveModel(model ai.Model) ai.Model {
	if a.modelReg == nil {
		return model
	}
	if def, ok := a.modelReg.Get(model.ID); ok && def.Provider == model.Provider {
		if def.SupportsImages != nil {
			model.SupportsImages = def.SupportsImages
		}
	}
	return model
}

// SubagentsEnabled opts the coding agent into the runtime task (subagent) tool.
func (CodingApplication) SubagentsEnabled() bool { return true }

// ToolNames returns the canonical coding-agent tool names.
func (CodingApplication) ToolNames(enableBash bool) []string {
	return codingtools.BaseToolNames(enableBash)
}

// Verify interface compliance at compile time.
var _ runtime.Application = CodingApplication{}

// WithRuntimeOverrides captures settings for a new session without replacing
// application-specific boot configuration or rediscovering gateway models.
func (a CodingApplication) WithRuntimeOverrides(cfg config.Config) runtime.Application {
	a.Cfg.EnableComputerUse = cfg.EnableComputerUse
	a.Cfg.ComputerApprovalPolicy = cfg.ComputerApprovalPolicy
	a.Cfg.ApprovedApps = append([]string(nil), cfg.ApprovedApps...)
	return a
}
