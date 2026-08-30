package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
)

func codeLanguage(config map[string]interface{}) (string, error) {
	raw, ok := config["language"]
	if !ok {
		return "python", nil
	}
	language, ok := raw.(string)
	if ok && language == "" {
		return "python", nil
	}
	if !ok || strings.TrimSpace(language) != language || (language != "python" && language != "typescript") {
		return "", fmt.Errorf("'language' must be one of \"python\" or \"typescript\"")
	}
	return language, nil
}

func resolveCodeRuntime(config map[string]interface{}) (language, interpreter string, err error) {
	language, err = codeLanguage(config)
	if err != nil {
		return "", "", err
	}
	if language == "typescript" {
		if override, ok := config["node_path"].(string); ok && strings.TrimSpace(override) != "" {
			path, reason := plugins.ResolveNodePath(override, ">=20.0")
			if path == "" {
				return "", "", fmt.Errorf("resolve node_path for TypeScript code node: %s", reason)
			}
			return language, path, nil
		}
		path, reason := plugins.ResolveNode(">=20.0")
		if path == "" {
			return "", "", fmt.Errorf("resolve Node >=20 for TypeScript code node: %s", reason)
		}
		return language, path, nil
	}
	if override, ok := config["python_path"].(string); ok && strings.TrimSpace(override) != "" {
		return language, override, nil
	}
	if path, _ := plugins.ResolvePython(""); path != "" {
		return language, path, nil
	}
	return language, "python3", nil
}

func validateCodeRuntimeConstraint(config map[string]interface{}) (string, error) {
	language, err := codeLanguage(config)
	if err != nil {
		return "", err
	}
	if language == "typescript" && !codeexec.PoolEnabled() {
		return "", fmt.Errorf("language 'typescript' requires the code worker pool; the legacy spawn path is Python-only")
	}
	return language, nil
}

func codeWrapperVersion(language string) int {
	if language == "typescript" {
		return codeexec.JSWrapperVersion()
	}
	return codeexec.WrapperVersion()
}
