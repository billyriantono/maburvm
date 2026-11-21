// Package agentenv reports the agent's configuration from its environment, so
// the panel's node page can show what a node runs with without a shell on it.
package agentenv

// setting is one variable the agent reads. A secret is reported only as "set".
type setting struct {
	name, def string
	secret    bool
}

// settings lists what the agent reads from its environment (systemd unit or
// install-agent.sh). Keep it in step with internal/agent; a variable missing
// here is simply not shown.
var settings = []setting{
	{name: "AGENT_BIND_ADDRESS", def: "127.0.0.1"},
	{name: "AGENT_GRPC_PORT", def: "50051"},
	{name: "AGENT_ALLOWED_PANEL_IPS"},
	{name: "AGENT_UPDATE_PUBKEY"},
	{name: "AGENT_CONSOLE_SHIELD", def: "on"},
	{name: "AGENT_TLS_CERT_FILE"},
	{name: "AGENT_TLS_KEY_FILE"},
	{name: "ENVIRONMENT", def: "development"},
	{name: "LIBVIRT_URI", def: "qemu:///system"},
	{name: "MABURVM_DATA_DIR", def: "/var/lib/maburvm"},
	{name: "MABURVM_BRIDGE_NETFILTER"},
	{name: "MABURVM_VLAN_BRIDGES"},
	{name: "MABURVM_NAT_ADDRESS"},
	{name: "MABURVM_MANAGED_DIR_ROOTS"},
	{name: "MABURVM_MANAGED_LVM_VGS"},
	{name: "MABURVM_MANAGED_ZFS_DATASETS"},
	{name: "STORAGE_ENDPOINT"},
	{name: "STORAGE_BUCKET"},
	{name: "STORAGE_REGION"},
	{name: "STORAGE_FORCE_HTTP"},
	{name: "STORAGE_USE_PATH_STYLE"},
	{name: "STORAGE_ACCESS_KEY", secret: true},
	{name: "STORAGE_SECRET_KEY", secret: true},
	// Read when the STORAGE_ name is unset; most nodes use these.
	{name: "S3_ENDPOINT"},
	{name: "S3_BUCKET"},
	{name: "S3_REGION"},
	{name: "S3_FORCE_HTTP"},
	{name: "S3_USE_PATH_STYLE"},
	{name: "S3_ACCESS_KEY", secret: true},
	{name: "S3_SECRET_KEY", secret: true},
	{name: "AGENT_AUTH_TOKEN", secret: true},
}

// Report maps every setting to its value: the environment's, else the
// agent's default marked "(default)", else "". A secret is "set" or "".
func Report(getenv func(string) string) map[string]string {
	out := make(map[string]string, len(settings))
	for _, s := range settings {
		v := getenv(s.name)
		switch {
		case s.secret && v != "":
			v = "set"
		case v == "" && s.def != "":
			v = s.def + " (default)"
		}
		out[s.name] = v
	}
	return out
}
