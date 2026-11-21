package apidoc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// The document is valid JSON, every reference in it points at something, and
// every operation is named, tagged and answerable.
func TestDocumentIsWhole(t *testing.T) {
	var doc js
	require.NoError(t, json.Unmarshal(Document(), &doc))
	assert.Equal(t, "3.1.0", doc["openapi"])

	components := doc["components"].(js)
	var refs []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case js:
			if r, ok := x["$ref"].(string); ok {
				refs = append(refs, r)
			}
			for _, k := range sortedKeys(x) {
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	require.NotEmpty(t, refs)
	for _, r := range refs {
		parts := strings.Split(strings.TrimPrefix(r, "#/components/"), "/")
		require.Len(t, parts, 2, r)
		_, found := components[parts[0]].(js)[parts[1]]
		assert.True(t, found, "%s points at nothing", r)
	}

	ids := map[string]bool{}
	paths := doc["paths"].(js)
	for _, p := range sortedKeys(paths) {
		for _, method := range sortedKeys(paths[p].(js)) {
			op := paths[p].(js)[method].(js)
			id, _ := op["operationId"].(string)
			assert.False(t, ids[id], "operationId %s is used twice", id)
			ids[id] = true
			assert.NotEmpty(t, op["summary"], "%s %s has no summary", method, p)
			success := false
			for code := range op["responses"].(js) {
				success = success || strings.HasPrefix(code, "2") || code == "302" || code == "405"
			}
			assert.True(t, success, "%s %s has no success answer", method, p)
			assert.NotNil(t, op["security"], "%s %s says nothing about credentials", method, p)
		}
	}
	webhooks := doc["webhooks"].(js)
	require.NotEmpty(t, webhooks)
	for _, name := range sortedKeys(webhooks) {
		op := webhooks[name].(js)["post"].(js)
		id, _ := op["operationId"].(string)
		assert.NotEmpty(t, id, "webhook %s has no operationId", name)
		assert.False(t, ids[id], "operationId %s is used twice", id)
		ids[id] = true
		assert.NotNil(t, op["security"], "webhook %s says nothing about credentials", name)
	}
	for _, tag := range doc["tags"].([]any) {
		assert.NotEmpty(t, tag.(js)["description"], "tag %v has no description", tag.(js)["name"])
	}
}

// Every permission a route names is one a staff role or a customer can hold.
func TestRoutesNameRealPermissions(t *testing.T) {
	known := map[string]bool{middleware.PermAll: true}
	for _, p := range middleware.GetPermissionsForRole(models.RoleClient) {
		known[p] = true
	}
	special := map[string]bool{"public": true, "signed-in": true, "staff": true, "token": true, "api-key": true, "webhook": true, "metrics": true, "owner": true}
	for _, r := range Routes() {
		if special[r.Access] {
			continue
		}
		for _, term := range strings.Fields(r.Access) {
			for _, p := range strings.Split(term, "|") {
				assert.True(t, middleware.InCatalog(p) || known[p], "%s %s names unknown permission %q", r.Method, r.Path, p)
			}
		}
	}
}
