package agent

import (
	"encoding/json"
	"log"
	"os"
	"strings"
)

// installIdentity is the part of the installation's config file this package
// needs. Only "name" is read: every other setting is already resolved by the
// entrypoint (cmd/probe/connect.go / cmd/probe-client/main.go), which owns the
// config schema. Duplicating the whole schema here would create a second owner
// and a second set of defaults to drift.
type installIdentity struct {
	Name string `json:"name"`
}

// configuredNameFromFile returns the agent identity written in the
// installation's config file at path, or "" when there is no file to read.
//
// This is the durable source of identity for an update, and it is why the name
// is read from disk rather than carried in the launch arguments:
//
//   - It is what the process reads on every start, so it survives a restart —
//     the promoted process re-registers under the same name after a reboot,
//     which a one-shot argument or an environment variable would not.
//   - It is the operator's configuration, so it does not change when an update
//     artifact replaces the binary.
//
// The artifact's EMBEDDED config cannot answer this: it is stamped with the
// identity of whoever built it (the build name — internal/server's
// generateConfigB64 writes build.Name into "name"), and every new build may
// carry a different one. That is exactly how a promotion ended up restoring
// the build's name instead of the installation's (P15-07).
//
// Empty is not an error: an embedded-config build with no file beside it has
// no file-backed identity, and the caller keeps the name it was launched with.
func configuredNameFromFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[identity] cannot read install config %s: %v (keeping the identity this process was launched with)", path, err)
		return ""
	}
	var cfg installIdentity
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[identity] cannot parse install config %s: %v (keeping the identity this process was launched with)", path, err)
		return ""
	}
	return strings.TrimSpace(cfg.Name)
}
