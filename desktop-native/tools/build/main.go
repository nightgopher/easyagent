// Build the Go core/resources before MyGo packages the native UI. Keeping this
// outside mygo.json's frontend buildCommand avoids enabling frontend bindings.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// targetPlatforms parses the -platform flag the way MyGo does (comma
// separated GOOS/GOARCH, defaulting to the host) so prepare can build the
// core for every requested target, not just the host pair. web and js/wasm
// targets need no core binary and are skipped.
func targetPlatforms(args []string) []string {
	host := runtime.GOOS + "/" + runtime.GOARCH
	requested := host
	for i, arg := range args {
		value := ""
		if arg == "-platform" && i+1 < len(args) {
			value = args[i+1]
		} else if strings.HasPrefix(arg, "-platform=") {
			value = strings.TrimPrefix(arg, "-platform=")
		}
		if value != "" {
			requested = value
		}
	}
	var platforms []string
	for _, entry := range strings.Split(requested, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "/", 2)
		if len(parts) != 2 || parts[0] == "web" || parts[0] == "js" {
			continue
		}
		if parts[1] == "universal" {
			// Universal macOS builds package both architectures.
			platforms = append(platforms, parts[0]+"/arm64", parts[0]+"/amd64")
			continue
		}
		platforms = append(platforms, entry)
	}
	return platforms
}

func main() {
	host := runtime.GOOS + "/" + runtime.GOARCH
	seen := map[string]bool{}
	var prepares [][]string
	for _, platform := range targetPlatforms(os.Args[1:]) {
		if seen[platform] {
			continue
		}
		seen[platform] = true
		env := []string{}
		if platform != host {
			env = append(env, "EASYAGENT_TARGET_PLATFORM="+strings.ReplaceAll(platform, "/", "-"))
		}
		prepares = append(prepares, env)
	}
	for _, env := range prepares {
		cmd := exec.Command("go", "run", "./tools/prepare")
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
		if runtime.GOOS == "darwin" && (len(env) == 0 || strings.HasPrefix(env[0], "EASYAGENT_TARGET_PLATFORM=darwin-")) {
			platform := runtime.GOOS + "-" + runtime.GOARCH
			if len(env) != 0 {
				platform = strings.TrimPrefix(env[0], "EASYAGENT_TARGET_PLATFORM=")
			}
			identity := ""
			for i, arg := range os.Args[1:] {
				if arg == "-sign" && i+2 < len(os.Args) {
					identity = os.Args[i+2]
				} else if strings.HasPrefix(arg, "-sign=") {
					identity = strings.TrimPrefix(arg, "-sign=")
				}
			}
			if identity == "" {
				var manifest struct {
					MacOS struct{ SigningIdentity string }
				}
				data, err := os.ReadFile("mygo.json")
				if err != nil || json.Unmarshal(data, &manifest) != nil {
					os.Exit(1)
				}
				identity = manifest.MacOS.SigningIdentity
			}
			args := []string{"--force", "--sign", identity, "--identifier", "com.hwj.easyagent.credentials"}
			if identity != "-" {
				args = append(args, "--options", "runtime", "--timestamp")
			}
			args = append(args, filepath.Join("resources", platform, "easyagent-credentials"))
			cmd := exec.Command("codesign", args...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if cmd.Run() != nil {
				os.Exit(1)
			}
		}
	}
	buildArgs := append([]string{"run", "github.com/egoist/mygo/cmd/mygo", "build"}, os.Args[1:]...)
	cmd := exec.Command("go", buildArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
