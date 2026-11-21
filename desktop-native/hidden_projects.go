package main

// Projects are derived from session metadata, so removing a sidebar project is a
// per-client visibility preference. It never deletes the workspace or sessions.
func (a *preview) setProjectHidden(key string, hidden bool) {
	l := a.Live
	if l.HiddenProjectsInvalid {
		l.Error = "移除项目列表无法读取，请先修复该文件"
		return
	}
	next := make(map[string]bool, len(l.HiddenProjects)+1)
	for k, value := range l.HiddenProjects {
		next[k] = value
	}
	if hidden {
		next[key] = true
	} else {
		delete(next, key)
	}
	version := l.Store.reserve("hidden-projects.json")
	if err := l.Store.save("hidden-projects.json", version, next); err != nil {
		l.Error = "保存项目列表失败：" + err.Error()
		return
	}
	l.HiddenProjects = next
}
