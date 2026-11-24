package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Config resolution sources, in the order ResolveConfigPath tries them. They
// are strings (not an enum) because they are logged and reported verbatim.
const (
	// ConfigSourceFlag: the operator passed --config explicitly.
	ConfigSourceFlag = "explicit --config"
	// ConfigSourceProbeClientJSON: probe-client.json sits beside the executable.
	ConfigSourceProbeClientJSON = "probe-client.json beside the executable"
	// ConfigSourceExeNamed: <executable basename>.json sits beside the executable.
	// This is the agent-named install layout (probe-V6049A-EAS1.exe +
	// probe-V6049A-EAS1.json) used by the builder.
	ConfigSourceExeNamed = "<executable basename>.json beside the executable"
	// ConfigSourceSingleJSON: exactly one .json file sits beside the executable.
	ConfigSourceSingleJSON = "the only .json file beside the executable"
	// ConfigSourceEmbedded: the settings are compiled into the binary
	// (-X main.configB64), so no file is needed at all.
	ConfigSourceEmbedded = "embedded build config"
)

// ResolveConfigPath returns the config file this process must actually use and
// names the rule that matched. The order is fixed:
//
//  1. explicit --config, when the file exists (wins over everything)
//  2. probe-client.json beside the executable
//  3. <executable basename>.json beside the executable (agent-named installs)
//  4. the only .json file beside the executable, when there is exactly one
//  5. the embedded build config, when the binary carries it
//
// The path returned in cases 1-4 is absolute, so a child process launched from
// a different working directory still opens the same file. Case 5 returns an
// empty path with source ConfigSourceEmbedded.
//
// A --config that names no file does NOT abort the search: it is logged as a
// warning naming the missing path and rules 2-5 still run. That is deliberate.
// v1.19.1 field measurement (V6049A-EAS1, 2026-11-24): the process running an
// update is the OLD client, which has no resolver and synthesises the default
// <dir>\probe-client.json for the canary. On an agent-named install that path
// never exists, while <exe>.json beside it does, so refusing to fall through
// turned a correct update into "Error: config not found: --config ...
// probe-client.json does not exist". A real operator typo is still loud — the
// warning reaches the console and the canary log — but it no longer loses a
// config file that is sitting in the same directory.
//
// When nothing resolves it returns a "config not found: ..." error naming every
// rule it tried. Callers that are about to spawn a canary MUST treat that error
// as fatal: the v1.19.1 field defect was a canary launched with no --config,
// which fell back to the default filename, printed "Error: could not read
// config file: probe-client.json" and exited while the update reported success.
func ResolveConfigPath(explicit, exePath string, hasEmbeddedConfig bool) (path, source string, err error) {
	// explicitMissing is the explicit --config that was given but is not on
	// disk. It is carried into the final error so the message still says the
	// flag was honoured, and then rules 2-5 get their chance.
	explicitMissing := ""
	if explicit != "" {
		abs, absErr := filepath.Abs(explicit)
		switch {
		case absErr != nil:
			explicitMissing = fmt.Sprintf("%s (%v)", explicit, absErr)
			log.Printf("[config] WARNING: explicit --config %s is unusable: %v; falling through to the config files beside the executable", explicit, absErr)
		case !isRegularFile(abs):
			explicitMissing = abs
			log.Printf("[config] WARNING: explicit --config %s does not exist; falling through to the config files beside the executable (an updater-synthesised default on an agent-named install looks exactly like this)", abs)
		default:
			return abs, ConfigSourceFlag, nil
		}
	}

	canonical := trimNextSuffix(exePath)
	if canonical == "" {
		if explicitMissing != "" {
			return "", "", fmt.Errorf("config not found: --config %s does not exist and the executable path is unknown", explicitMissing)
		}
		return "", "", fmt.Errorf("config not found: executable path is unknown")
	}
	dir := filepath.Dir(canonical)

	beside := filepath.Join(dir, "probe-client.json")
	if isRegularFile(beside) {
		return beside, ConfigSourceProbeClientJSON, nil
	}

	exeNamed := exeJSONName(canonical)
	namedPath := filepath.Join(dir, exeNamed)
	if isRegularFile(namedPath) {
		return namedPath, ConfigSourceExeNamed, nil
	}

	jsons, listErr := jsonFilesIn(dir)
	if listErr == nil && len(jsons) == 1 {
		return jsons[0], ConfigSourceSingleJSON, nil
	}
	if hasEmbeddedConfig {
		return "", ConfigSourceEmbedded, nil
	}

	candidates := "no other .json file beside it"
	if listErr == nil && len(jsons) > 1 {
		candidates = fmt.Sprintf("%d .json files beside it (ambiguous)", len(jsons))
	}
	if explicitMissing != "" {
		// Every rule is named, including the explicit flag that was given and
		// found nothing: the operator must not have to guess which file the
		// process wanted.
		return "", "", fmt.Errorf(
			"config not found: --config %s does not exist, no probe-client.json beside %s, no %s beside it, and %s; refusing to start a canary that cannot read its config",
			explicitMissing, dir, exeNamed, candidates)
	}
	return "", "", fmt.Errorf(
		"config not found: no --config, no probe-client.json beside %s, no %s beside it, and %s; refusing to start a canary that cannot read its config",
		dir, exeNamed, candidates)
}

// exeJSONName returns the config filename that mirrors the executable's own
// name: probe.exe -> probe.json, probe-V6049A-EAS1.exe -> probe-V6049A-EAS1.json.
func exeJSONName(exePath string) string {
	base := filepath.Base(exePath)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base + ".json"
}

// jsonFilesIn returns the regular .json files in dir, sorted by name.
func jsonFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out, nil
}

// isRegularFile reports whether path exists and is not a directory.
func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// resolvedConfigPath returns the config path this agent must hand to a canary.
// The path resolved at startup (Config.ConfigPath) wins while it still exists;
// otherwise resolution is repeated against the running executable so an agent
// started without --config still finds its agent-named config file.
func (a *Agent) resolvedConfigPath() (string, error) {
	if a.cfg.ConfigPath != "" {
		if abs, err := filepath.Abs(a.cfg.ConfigPath); err == nil && isRegularFile(abs) {
			return abs, nil
		}
		log.Printf("[update] startup config path %q is no longer readable; re-resolving beside the executable", a.cfg.ConfigPath)
	}
	exe, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("config not found: cannot determine executable path: %v", err)
	}
	path, source, rerr := ResolveConfigPath("", exe, a.cfg.EmbeddedConfig)
	if rerr != nil {
		return "", rerr
	}
	log.Printf("[update] resolved config path for canary: %q (%s)", path, source)
	return path, nil
}
