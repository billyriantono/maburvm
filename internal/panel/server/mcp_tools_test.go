package server

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/apidoc"
	"github.com/maburvm/panel/internal/panel/handler"
)

// Each MCP tool names exactly the permissions its REST route checks, so
// tools/list hides the tools the route would refuse and shows the rest.
func TestMCPToolPermissionsMatchTheirRoutes(t *testing.T) {
	docs := map[string]apidoc.Route{}
	for _, r := range apidoc.Routes() {
		docs[r.Method+" "+r.Path] = r
	}
	for _, tool := range handler.MCPToolRoutes() {
		r, ok := docs[tool.Method+" "+tool.Path]
		require.True(t, ok, "%s maps to %s %s, which is not a documented route", tool.Name, tool.Method, tool.Path)
		switch r.Access {
		case "signed-in":
			assert.Empty(t, tool.Perms, "%s: its route is open to any account", tool.Name)
			continue
		case "public", "staff", "token", "api-key", "webhook", "metrics", "owner":
			t.Errorf("%s: route access %q is not one a tool should map to", tool.Name, r.Access)
			continue
		}
		terms := strings.Fields(r.Access)
		assert.Len(t, tool.Perms, len(terms), "%s: route needs %q, tool names %v", tool.Name, r.Access, tool.Perms)
		for _, term := range terms {
			alts := strings.Split(term, "|")
			assert.True(t, slices.ContainsFunc(tool.Perms, func(p string) bool { return slices.Contains(alts, p) }),
				"%s: route needs %s, tool names %v", tool.Name, term, tool.Perms)
		}
	}
}
