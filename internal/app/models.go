package app

import "github.com/hwj123hwj/easyagent/sdk/ai"

// ResolveModel exposes application catalog metadata without coupling the SDK
// runtime to the internal model registry.
func (a *App) ResolveModel(model ai.Model) ai.Model {
	if resolver, ok := a.ResolveApplication("").(interface{ ResolveModel(ai.Model) ai.Model }); ok {
		return resolver.ResolveModel(model)
	}
	return model
}
