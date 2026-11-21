package agentenv

import "testing"

func TestReportNeverLeaksSecrets(t *testing.T) {
	env := map[string]string{
		"AGENT_AUTH_TOKEN":     "node-token-value",
		"STORAGE_SECRET_KEY":   "very-secret",
		"S3_SECRET_KEY":        "very-secret",
		"STORAGE_ENDPOINT":     "s3.example.com",
		"MABURVM_VLAN_BRIDGES": "br0",
	}
	got := Report(func(k string) string { return env[k] })
	for k, want := range map[string]string{
		"AGENT_AUTH_TOKEN":     "set",
		"STORAGE_SECRET_KEY":   "set",
		"STORAGE_ACCESS_KEY":   "",
		"S3_SECRET_KEY":        "set",
		"STORAGE_ENDPOINT":     "s3.example.com",
		"MABURVM_VLAN_BRIDGES": "br0",
		"AGENT_BIND_ADDRESS":   "127.0.0.1 (default)",
		"AGENT_UPDATE_PUBKEY":  "",
	} {
		if got[k] != want {
			t.Fatalf("%s = %q, want %q", k, got[k], want)
		}
	}
	for k, v := range got {
		if v == "node-token-value" || v == "very-secret" {
			t.Fatalf("%s leaked a secret", k)
		}
	}
}
