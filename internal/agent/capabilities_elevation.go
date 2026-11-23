package agent

// CapExecSystem is advertised by every v1.19+ agent. It means "this build
// implements the as:\"SYSTEM\" contract" — the server gates the parameter on it
// so an old agent (which would silently ignore the unknown field and run the
// command unelevated as flk) is refused instead of producing a false success.
const CapExecSystem = "exec.system"

// defaultCapabilities is what a v1.19 agent advertises when the config leaves
// Capabilities empty (empty historically meant "all"). The list mirrors the
// commands this binary implements plus the new contract marker.
var defaultCapabilities = []string{
	"exec", "filesystem", "process", "tunnel", "mitm", "debug",
	"capture", "input", "clipboard", CapExecSystem,
}

// advertisedCapabilities returns the capability list to publish on connect.
// An explicit config list is honoured but always gains CapExecSystem: the
// marker describes the binary, not the operator's policy, and without it the
// server would refuse every elevated call from a correctly declared node.
func (a *Agent) advertisedCapabilities() []string {
	src := a.cfg.Capabilities
	if len(src) == 0 {
		out := make([]string, len(defaultCapabilities))
		copy(out, defaultCapabilities)
		return out
	}
	for _, c := range src {
		if c == CapExecSystem {
			return src
		}
	}
	out := make([]string, 0, len(src)+1)
	out = append(out, src...)
	out = append(out, CapExecSystem)
	return out
}
