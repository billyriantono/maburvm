// Package apidoc builds the OpenAPI document for the panel's HTTP API. Routes
// are listed in routes.go; request and response schemas come from the Go types
// the handlers bind and return, so a renamed field shows up here by itself.
// The server package's TestOpenAPICoversEveryRoute fails when a registered
// route is missing, and TestOpenAPIAccessMatchesMiddleware when a route's
// documented permissions are not what its middleware enforces.
package apidoc

import (
	"encoding"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type js = map[string]any

// apiRoute documents one route.
type apiRoute struct {
	method, path string
	tag          string
	summary      string
	desc         string
	// access is "public", "signed-in" (any account, acting on itself),
	// "staff" (any staff account), "token" (node enrollment token),
	// "api-key" (API keys only), "webhook" (billing key and signature),
	// "metrics" (metrics token), "owner" (the founding administrator only),
	// or staff permissions: space-separated terms
	// that are all required, each term "a|b" when either will do.
	access   string
	customer bool // customers may call it too, on their own resources
	query    []string
	body     any
	resp     any
	status   int
	// mime is the success content type when it is not JSON.
	mime    string
	limited bool // per-address rate limit (sign-in and node enrollment)
	// browserOnly routes take the session cookie only, never a token or key.
	browserOnly bool
}

func route(method, path, summary string) *apiRoute {
	return &apiRoute{method: method, path: path, summary: summary, access: "staff"}
}
func get(path, summary string) *apiRoute  { return route(http.MethodGet, path, summary) }
func post(path, summary string) *apiRoute { return route(http.MethodPost, path, summary) }
func put(path, summary string) *apiRoute  { return route(http.MethodPut, path, summary) }
func del(path, summary string) *apiRoute  { return route(http.MethodDelete, path, summary) }

func (r *apiRoute) can(access string) *apiRoute { r.access = access; return r }
func (r *apiRoute) cust() *apiRoute             { r.customer = true; return r }
func (r *apiRoute) q(names ...string) *apiRoute { r.query = append(r.query, names...); return r }
func (r *apiRoute) in(body any) *apiRoute       { r.body = body; return r }
func (r *apiRoute) out(resp any) *apiRoute      { r.resp = resp; return r }
func (r *apiRoute) code(status int) *apiRoute   { r.status = status; return r }
func (r *apiRoute) note(d string) *apiRoute     { r.desc = d; return r }
func (r *apiRoute) limit() *apiRoute            { r.limited = true; return r }
func (r *apiRoute) raw(mime string) *apiRoute   { r.mime = mime; return r }
func (r *apiRoute) browser() *apiRoute          { r.browserOnly = true; return r }

// group sets the tag of a run of routes.
func group(tag string, routes ...*apiRoute) []*apiRoute {
	for _, r := range routes {
		r.tag = tag
	}
	return routes
}

// Schema shorthands for hand-written shapes.
var (
	tStr  = js{"type": "string"}
	tInt  = js{"type": "integer"}
	tNum  = js{"type": "number"}
	tBool = js{"type": "boolean"}
	tTime = js{"type": "string", "format": "date-time"}
	tUUID = js{"type": "string", "format": "uuid"}
	tAny  = js{}
	tTrue = js{"type": "boolean", "const": true}
)

// obj builds an object schema from name, schema pairs.
func obj(pairs ...any) js {
	props := js{}
	for i := 0; i+1 < len(pairs); i += 2 {
		props[pairs[i].(string)] = pairs[i+1]
	}
	return js{"type": "object", "properties": props}
}

func arr(items any) js { return js{"type": "array", "items": items} }

func described(s js, d string) js {
	out := js{}
	for k, v := range s {
		out[k] = v
	}
	out["description"] = d
	return out
}

// Response envelopes. New routes answer ok, okList or a paginated list with
// meta; older ones keep their own fields for the clients that read them, and
// their paginated lists add success and meta beside those fields.
func ok(data any) js { return obj("success", tTrue, "data", data) }
func okList(item any) js {
	return obj("success", tTrue, "data", arr(item))
}

// pageMeta is the pagination block every paginated list carries.
var pageMeta = described(obj("page", tInt, "per_page", tInt, "total", js{"type": "integer", "format": "int64"}),
	"Pagination: the page (from 1), the items per page, and the items in all pages.")

var (
	okMsg  = obj("success", tTrue, "message", tStr)
	okOnly = obj("success", tTrue)
	msg    = obj("message", tStr)
)

func msgData(data any) js { return obj("message", tStr, "data", data) }
func bare(data any) js    { return obj("data", data) }

// okListMeta is a list answered whole unless limit/offset or page/page_size
// ask for a window; meta says which part it is.
func okListMeta(item any) js { return obj("success", tTrue, "data", arr(item), "meta", pageMeta) }

// okPaged is ok with meta beside data, for paginated lists nested in data.
func okPaged(data any) js { return obj("success", tTrue, "meta", pageMeta, "data", data) }

// pageOffset is the limit/offset list (backups, snapshots).
func pageOffset(item any) js {
	return obj("success", tTrue, "message", tStr, "data", arr(item), "meta", pageMeta, "total", tInt, "limit", tInt, "offset", tInt, "has_more", tBool)
}

// pageNum is the page/page_size list (users, audit log).
func pageNum(item any) js {
	return obj("success", tTrue, "data", arr(item), "meta", pageMeta, "total", tInt, "page", tInt, "page_size", tInt, "totalPages", tInt)
}

// pageIn is the list nested under data (alerts, deliveries, emails, queue).
func pageIn(key string, item any) js {
	return obj("success", tTrue, "meta", pageMeta, "data", obj(key, arr(item), "total", tInt, "page", tInt, "page_size", tInt))
}

var jobQueued = obj("job_id", tStr, "status", tStr)

// queryParams documents the query parameters routes share.
var queryParams = map[string]js{
	"page":      {"schema": js{"type": "integer", "minimum": 1}, "description": "Page number, from 1."},
	"page_size": {"schema": js{"type": "integer", "minimum": 1}, "description": "Items per page."},
	"per_page":  {"schema": js{"type": "integer", "minimum": 1}, "description": "Items per page; the same as page_size, which wins when both are sent."},
	"limit":     {"schema": js{"type": "integer", "minimum": 1}, "description": "Items to return."},
	"offset":    {"schema": js{"type": "integer", "minimum": 0}, "description": "Items to skip."},
	"search":    {"schema": tStr, "description": "Text to match."},
	"q":         {"schema": tStr, "description": "Text to match."},
	"status":    {"schema": tStr, "description": "Only items in this status."},
	"node_id":   {"schema": tUUID, "description": "Only items on this node."},
	"user_id":   {"schema": tUUID, "description": "Only items of this account (staff only)."},
	"state":     {"schema": tStr, "description": "Only items in this state."},
	"minutes":   {"schema": tInt, "description": "How far back to read, in minutes."},
	"all":       {"schema": tBool, "description": "Include every item, not only flagged ones."},
}

var (
	timeType      = reflect.TypeOf(time.Time{})
	uuidType      = reflect.TypeOf(uuid.UUID{})
	rawJSONType   = reflect.TypeOf(json.RawMessage{})
	deletedAtType = reflect.TypeOf(gorm.DeletedAt{})
	marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textType      = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// docGen turns Go types into component schemas.
type docGen struct {
	schemas js
	names   map[string]reflect.Type
}

// schema is the JSON schema of v: nil (anything), a hand-written js, or a Go
// value whose type is reflected.
func (g *docGen) schema(v any) js {
	switch s := v.(type) {
	case nil:
		return tAny
	case js:
		return g.walk(s)
	}
	return g.typ(reflect.TypeOf(v))
}

// walk reflects Go values nested inside a hand-written schema.
func (g *docGen) walk(s js) js {
	out := js{}
	for k, v := range s {
		switch k {
		case "properties":
			props := js{}
			for name, p := range v.(js) {
				props[name] = g.schema(p)
			}
			out[k] = props
		case "items", "additionalProperties":
			out[k] = g.schema(v)
		default:
			out[k] = v
		}
	}
	return out
}

func nullable(s js) js {
	if t, ok := s["type"].(string); ok {
		out := js{}
		for k, v := range s {
			out[k] = v
		}
		out["type"] = []any{t, "null"}
		return out
	}
	if _, ok := s["$ref"]; ok {
		return js{"anyOf": []any{s, js{"type": "null"}}}
	}
	return s
}

func (g *docGen) typ(t reflect.Type) js {
	switch t {
	case timeType:
		return tTime
	case uuidType:
		return tUUID
	case rawJSONType:
		return tAny
	case deletedAtType:
		return js{"type": []any{"string", "null"}, "format": "date-time"}
	}
	if t.Kind() == reflect.Pointer {
		return nullable(g.typ(t.Elem()))
	}
	if t.Implements(marshalerType) || reflect.PointerTo(t).Implements(marshalerType) {
		return described(tAny, "Encoded by its own JSON method.")
	}
	if t.Implements(textType) || reflect.PointerTo(t).Implements(textType) {
		return tStr
	}
	switch t.Kind() {
	case reflect.Bool:
		return tBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return tInt
	case reflect.Int64, reflect.Uint64:
		return js{"type": "integer", "format": "int64"}
	case reflect.Float32, reflect.Float64:
		return tNum
	case reflect.String:
		return tStr
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return js{"type": "string", "contentEncoding": "base64"}
		}
		return arr(g.typ(t.Elem()))
	case reflect.Map:
		return js{"type": "object", "additionalProperties": g.typ(t.Elem())}
	case reflect.Struct:
		if t.Name() == "" {
			return g.structSchema(t)
		}
		return g.ref(t)
	}
	return tAny
}

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9]+`)

// ref registers t as a component and points at it. A name two packages share
// takes the package name in front.
func (g *docGen) ref(t reflect.Type) js {
	name := nonIdent.ReplaceAllString(t.Name(), "")
	if other, taken := g.names[name]; taken && other != t {
		pkg := t.PkgPath()[strings.LastIndex(t.PkgPath(), "/")+1:]
		name = string(unicode.ToUpper(rune(pkg[0]))) + pkg[1:] + name
	}
	if _, done := g.names[name]; !done {
		g.names[name] = t
		g.schemas[name] = js{} // placeholder: a type may refer to itself
		g.schemas[name] = g.structSchema(t)
	}
	return js{"$ref": "#/components/schemas/" + name}
}

func (g *docGen) structSchema(t reflect.Type) js {
	props := js{}
	g.fields(t, props)
	return js{"type": "object", "properties": props}
}

func (g *docGen) fields(t reflect.Type, props js) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && opts == "" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != timeType {
				g.fields(ft, props)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		s := g.typ(f.Type)
		if strings.Contains(opts, "string") {
			s = tStr
		}
		props[name] = s
	}
}

// pathParam turns /vms/:id into /vms/{id}.
var pathParam = regexp.MustCompile(`:([A-Za-z_]+)`)

func openAPIPath(p string) string {
	p = pathParam.ReplaceAllString(p, "{$1}")
	return strings.ReplaceAll(p, "*", "{path}")
}

func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/api/v1"), "/") {
		by := strings.HasPrefix(seg, ":")
		seg = strings.TrimPrefix(seg, ":")
		if seg == "" {
			continue
		}
		if by {
			b.WriteString("By")
		}
		for _, w := range nonIdent.Split(seg, -1) {
			if w != "" {
				b.WriteString(strings.ToUpper(w[:1]) + w[1:])
			}
		}
	}
	return b.String()
}

var (
	signedInSecurity = []any{js{"session": []any{}}, js{"bearer": []any{}}, js{"apiKey": []any{}}}
	apiKeySecurity   = []any{js{"bearer": []any{}}, js{"apiKey": []any{}}}
)

// accessText says in words who may call a route.
func accessText(r *apiRoute) string {
	var who string
	switch r.access {
	case "public":
		who = "Public: no credentials."
	case "signed-in":
		who = "Any signed-in account, staff or customer, acting on its own account."
	case "staff":
		who = "Any staff account, whatever its role."
	case "token":
		who = "A node enrollment token (`Authorization: Bearer mbe_...`) from the Add node install command."
	case "api-key":
		who = "API keys only (`X-API-Key` or `Authorization: Bearer mvk_...`). Each tool call is checked like the REST route it maps to."
	case "webhook":
		who = "The billing API key in `X-API-Key` and an HMAC-SHA256 signature of the raw body in `X-Webhook-Signature`."
	case "metrics":
		who = "The metrics token as `Authorization: Bearer <token>`; off while no token is set."
	case "owner":
		who = "Only the Owner, the founding administrator."
	default:
		var terms []string
		for _, t := range strings.Fields(r.access) {
			alts := strings.Split(t, "|")
			for i := range alts {
				alts[i] = "`" + alts[i] + "`"
			}
			terms = append(terms, strings.Join(alts, " or "))
		}
		who = "Staff with " + strings.Join(terms, " and ") + "."
		if strings.Contains(r.access, "admin:access") {
			who += " `admin:access` is the Administrator and Owner roles; a custom role cannot hold it."
		}
		if strings.Contains(r.access, "staff:manage") {
			who += " Only the Owner, or a role that may assign staff roles."
		}
	}
	if r.customer {
		who += " Customers too, on their own resources only; another tenant's resource answers 404."
	}
	if r.browserOnly {
		who += " From the staff member's own browser session only: tokens and API keys are refused."
	}
	return who
}

// openAPIDocument builds the whole document.
func openAPIDocument(routes []*apiRoute) js {
	g := &docGen{schemas: js{}, names: map[string]reflect.Type{}}
	paths := js{}
	tagSeen := map[string]bool{}
	var tags []any
	for _, r := range routes {
		if !tagSeen[r.tag] {
			tagSeen[r.tag] = true
			tags = append(tags, js{"name": r.tag, "description": apiTagDescriptions[r.tag]})
		}
		p := openAPIPath(r.path)
		item, _ := paths[p].(js)
		if item == nil {
			item = js{}
			paths[p] = item
		}
		op := js{
			"operationId":    operationID(r.method, r.path),
			"summary":        r.summary,
			"tags":           []any{r.tag},
			"x-access":       r.access,
			"x-customer":     r.customer,
			"x-rate-limited": r.limited,
		}
		desc := "**Access:** " + accessText(r)
		if r.desc != "" {
			desc = r.desc + "\n\n" + desc
		}
		op["description"] = desc

		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(r.path, -1) {
			params = append(params, js{"name": m[1], "in": "path", "required": true, "schema": tStr})
		}
		if strings.Contains(r.path, "*") {
			params = append(params, js{"name": "path", "in": "path", "required": true, "schema": tStr})
		}
		query := r.query
		for _, name := range r.query {
			if name == "page_size" {
				query = append(append([]string{}, r.query...), "per_page")
			}
		}
		for _, name := range query {
			qp := js{"name": name, "in": "query", "schema": tStr}
			for k, v := range queryParams[name] {
				qp[k] = v
			}
			params = append(params, qp)
		}
		if r.access == "webhook" {
			params = append(params, js{"name": "X-Webhook-Signature", "in": "header", "required": true, "schema": tStr,
				"description": "Hex HMAC-SHA256 of the raw request body with the billing webhook secret; a `sha256=` prefix is accepted."})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}

		switch r.access {
		case "public":
			op["security"] = []any{}
		case "token":
			op["security"] = []any{js{"enrollmentToken": []any{}}}
		case "api-key":
			op["security"] = apiKeySecurity
		case "webhook":
			op["security"] = []any{js{"billingKey": []any{}}}
		case "metrics":
			op["security"] = []any{js{"metricsToken": []any{}}}
		default:
			op["security"] = signedInSecurity
			if r.browserOnly {
				op["security"] = []any{js{"session": []any{}}}
			}
		}

		if r.body != nil {
			op["requestBody"] = js{"required": true, "content": js{"application/json": js{"schema": g.schema(r.body)}}}
		}

		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		success := js{"description": http.StatusText(status)}
		switch {
		case status == http.StatusNoContent || status == http.StatusFound:
		case r.mime != "":
			s := g.schema(r.resp)
			if r.resp == nil {
				s = js{"type": "string"}
			}
			success["content"] = js{r.mime: js{"schema": s}}
		default:
			success["content"] = js{"application/json": js{"schema": g.schema(r.resp)}}
		}
		responses := js{itoa(status): success}
		if r.body != nil || len(r.query) > 0 || len(params) > 0 {
			responses["400"] = js{"$ref": "#/components/responses/BadRequest"}
		}
		if r.access != "public" {
			responses["401"] = js{"$ref": "#/components/responses/Unauthorized"}
			responses["403"] = js{"$ref": "#/components/responses/Forbidden"}
		}
		if len(pathParam.FindAllString(r.path, -1)) > 0 {
			responses["404"] = js{"$ref": "#/components/responses/NotFound"}
		}
		if r.access != "public" || r.limited {
			responses["429"] = js{"$ref": "#/components/responses/TooManyRequests"}
		}
		responses["default"] = js{"$ref": "#/components/responses/Error"}
		op["responses"] = responses
		item[strings.ToLower(r.method)] = op
	}

	webhooks := js{}
	for name, w := range apiWebhooks {
		webhooks[name] = js{"post": js{
			"operationId": "webhook" + strings.ToUpper(name[:1]) + name[1:],
			"summary":     w.summary,
			"description": w.desc,
			"tags":        []any{"Webhooks"},
			// The panel calls you: no credential of yours, only its signature.
			"security": []any{},
			"parameters": []any{js{"name": "X-Webhook-Signature", "in": "header", "schema": tStr,
				"description": "Hex HMAC-SHA256 of the raw body with the secret saved in the panel, no prefix. Absent when no secret is saved."}},
			"requestBody": js{"required": true, "content": js{"application/json": js{"schema": g.schema(w.body)}}},
			"responses":   js{"2XX": js{"description": "Received. Anything else, or no answer within ten seconds, is tried again."}},
		}}
	}

	return js{
		"openapi": "3.1.0",
		"info": js{
			"title":       "MaburVM API",
			"version":     "1",
			"description": apiOverview,
			"license":     js{"name": "MIT", "identifier": "MIT"},
		},
		"servers":  []any{js{"url": "/", "description": "This panel"}},
		"tags":     append(tags, js{"name": "Webhooks", "description": apiTagDescriptions["Webhooks"]}),
		"paths":    paths,
		"webhooks": webhooks,
		"components": js{
			"schemas": mergeSchemas(g.schemas, js{"Error": errorSchema}),
			"responses": js{
				"BadRequest":      errResponse("The request is not valid: a missing or malformed field, or an action the resource's state does not allow."),
				"Unauthorized":    errResponse("No valid session, token or API key. A suspended account answers with `code: account_suspended`."),
				"Forbidden":       errResponse("The account may not do this: a missing permission (`message: Insufficient permissions`), a read-only or narrowed API key, a refused IP address, a cross-site browser write, two-factor setup required (`code: two_factor_required`), or a step staff may not take while viewing as a customer (`code: impersonation_forbidden`)."),
				"NotFound":        errResponse("No such resource, or one the caller may not see (customers get 404 for another tenant's resources)."),
				"TooManyRequests": js{"description": "Over the rate limit. `Retry-After` gives the seconds to wait.", "headers": js{"Retry-After": js{"schema": tInt}}, "content": js{"application/json": js{"schema": js{"$ref": "#/components/schemas/Error"}}}},
				"Error":           errResponse("Any other failure, including 409 when the resource is busy or in conflict and 503 when a dependency (a node, the job queue, the billing webhook) is unavailable."),
			},
			"securitySchemes": js{
				"session":         js{"type": "apiKey", "in": "cookie", "name": "accessToken", "description": "The browser session cookie set by `POST /api/v1/auth/login` (HttpOnly, SameSite=Lax). Writes carrying it from another site are refused."},
				"bearer":          js{"type": "http", "scheme": "bearer", "bearerFormat": "JWT or mvk_ API key", "description": "The `token` from sign-in (a JWT), or an API key (`mvk_...`)."},
				"apiKey":          js{"type": "apiKey", "in": "header", "name": "X-API-Key", "description": "An API key (`mvk_...`) made under Settings, API keys."},
				"enrollmentToken": js{"type": "http", "scheme": "bearer", "bearerFormat": "mbe_ enrollment token", "description": "Single-use node enrollment token, valid for one hour."},
				"billingKey":      js{"type": "apiKey", "in": "header", "name": "X-API-Key", "description": "The billing webhook API key (System settings, Panel, Billing webhook, or BILLING_API_KEY)."},
				"metricsToken":    js{"type": "http", "scheme": "bearer", "description": "The metrics token (System settings, Panel, or METRICS_TOKEN)."},
			},
		},
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func mergeSchemas(a, b js) js {
	for k, v := range b {
		a[k] = v
	}
	return a
}

var errorSchema = js{
	"type": "object",
	"description": "Every error answer: `success` is false, `error.code` is a stable machine code (a route's own, such as `two_factor_required`, " +
		"else one for the HTTP status: `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `rate_limited`, `unavailable`, `internal_error`...), " +
		"`error.message` says what went wrong in words and `error.details` is there when the route has more to say. " +
		"The top-level `message` repeats `error.message`, and a route's other top-level fields (such as `code` or `request_id`) stay where they were.",
	"required": []any{"success", "error", "message"},
	"properties": js{
		"success": js{"type": "boolean", "const": false},
		"error": js{"type": "object", "required": []any{"code", "message"}, "properties": js{
			"code":    tStr,
			"message": tStr,
			"details": described(tAny, "More about the error, when the route gives it."),
		}},
		"message": tStr,
		"code":    described(tStr, "Kept on the routes that sent a top-level code before the envelope: the same as error.code."),
	},
}

func errResponse(d string) js {
	return js{"description": d, "content": js{"application/json": js{"schema": js{"$ref": "#/components/schemas/Error"}}}}
}

type apiWebhook struct {
	summary, desc string
	body          any
}

var (
	docOnce sync.Once
	docJSON []byte
)

// Document is the OpenAPI document as JSON.
func Document() []byte {
	docOnce.Do(func() {
		docJSON, _ = json.Marshal(openAPIDocument(apiRoutes()))
	})
	return docJSON
}

// Handler serves the document at /api/v1/openapi.json. It is public: it
// describes the API's shape, the same on every install and already readable
// in the open source, and holds no data, address or secret, so tools and
// integrators can fetch it before they have credentials.
func Handler(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.JSONBlob(http.StatusOK, Document())
}

// Route is what a test needs to hold a documented route to the real one.
type Route struct {
	Method, Path string
	// Access is as described on apiRoute.access.
	Access      string
	Customer    bool
	BrowserOnly bool
}

// Routes lists every documented route, in document order.
func Routes() []Route {
	var out []Route
	for _, r := range apiRoutes() {
		out = append(out, Route{Method: r.method, Path: r.path, Access: r.access, Customer: r.customer, BrowserOnly: r.browserOnly})
	}
	return out
}

// sortedKeys is for deterministic iteration in tests.
func sortedKeys(m js) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
