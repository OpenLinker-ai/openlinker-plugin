package buildinfo

const SurfaceVersion = "openlinker.plugin-host.v1"

// Version is overridden by release builds through -ldflags.
var Version = "dev"
var Revision = "unknown"

func Capabilities() []string {
	return []string{"agent.configure", "agent.doctor", "agent.serve", "agent.status", "plugin.serve", "plugin.browser.serve"}
}
