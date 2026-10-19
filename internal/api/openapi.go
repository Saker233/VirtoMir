package api

import (
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/onurbaydas/virtomir/internal/auth"
)

// routeMux is the ServeMux that also remembers every route, so the OpenAPI description at
// GET /api/v1/openapi.json always matches what the server really serves.
type routeMux struct {
	*http.ServeMux
	routes []route
}

type route struct {
	method, path, handler string
}

func newRouteMux() *routeMux { return &routeMux{ServeMux: http.NewServeMux()} }

func (m *routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.ServeMux.HandleFunc(pattern, h)
	if method, path, ok := strings.Cut(pattern, " "); ok && strings.HasPrefix(path, "/api/") {
		m.routes = append(m.routes, route{method: method, path: path, handler: handlerName(h)})
	}
}

// handlerName turns "…/api.(*Server).listDomains-fm" into "listDomains".
func handlerName(h any) string {
	f := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
	if f == nil {
		return ""
	}
	n := f.Name()
	if i := strings.LastIndex(n, "."); i >= 0 {
		n = n[i+1:]
	}
	n = strings.TrimSuffix(n, "-fm")
	if strings.HasPrefix(n, "func") { // an inline closure says nothing
		return ""
	}
	return n
}

// camelWords splits "addDHCPHost" into add, DHCP, Host.
func camelWords(s string) []string {
	r := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(r); i++ {
		lowerToUpper := unicode.IsUpper(r[i]) && !unicode.IsUpper(r[i-1])
		acronymEnd := unicode.IsUpper(r[i]) && unicode.IsUpper(r[i-1]) && i+1 < len(r) && unicode.IsLower(r[i+1])
		if lowerToUpper || acronymEnd {
			words = append(words, string(r[start:i]))
			start = i
		}
	}
	if start < len(r) {
		words = append(words, string(r[start:]))
	}
	return words
}

// sentence makes "listDomains" readable: "List domains".
func sentence(name string) string {
	words := camelWords(name)
	for i, w := range words {
		if i > 0 && !(len(w) > 1 && strings.ToUpper(w) == w) { // keep acronyms (XML, DHCP)
			words[i] = strings.ToLower(w)
		}
	}
	s := strings.Join(words, " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

var pathParam = regexp.MustCompile(`\{([a-zA-Z]+)(\.\.\.)?\}`)

// tagOf groups endpoints by their first path segment after /api/v1.
func tagOf(path string) string {
	rest := strings.TrimPrefix(path, "/api/v1/")
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" {
		return "general"
	}
	return seg
}

// projectSchema describes a project (ADR-0060) in the OpenAPI document; machines carry
// their project as the "project" field (empty = global).
func projectSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          str(),
			"name":        str(),
			"description": str(),
			"members": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "user or group"},
				"name": str(),
				"role": map[string]any{"type": "string", "description": "viewer, operator or admin"},
			}}},
			"limits":       map[string]any{"type": "object", "description": "machines, vcpus, memoryMiB, diskGiB; zero = unlimited"},
			"storagePools": map[string]any{"type": "array", "items": str()},
			"networks":     map[string]any{"type": "array", "items": str()},
			"created":      str(),
		},
	}
}

func complianceResultSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          str(),
			"title":       str(),
			"category":    str(),
			"severity":    map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low"}},
			"status":      map[string]any{"type": "string", "enum": []string{"pass", "fail", "warn", "error"}},
			"detail":      str(),
			"remediation": str(),
			"benchmark":   str(),
		},
	}
}

func hostComplianceSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "integer"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"hostId":       str(),
			"hostName":     str(),
			"evaluatedAt":  map[string]any{"type": "string", "format": "date-time"},
			"score":        num(),
			"passedCount":  num(),
			"failedCount":  num(),
			"warningCount": num(),
			"totalCount":   num(),
			"results": map[string]any{
				"type":  "array",
				"items": map[string]any{"$ref": "#/components/schemas/complianceResult"},
			},
		},
	}
}

func fipsInfoSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kernelEnabled": boolean(),
			"enforced":      boolean(),
			"tlsMinVersion": str(),
			"cipherSuites":  map[string]any{"type": "array", "items": str()},
			"curves":        map[string]any{"type": "array", "items": str()},
			"description":   str(),
		},
	}
}

func auditVerificationSchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	num := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"valid":         boolean(),
			"verifiedCount": num(),
			"firstSeq":      num(),
			"lastSeq":       num(),
			"headHash":      str(),
			"tamperedSeq":   num(),
			"tamperedIndex": num(),
			"reason":        str(),
		},
	}
}

// openAPI builds an OpenAPI 3.1 document of the registered routes: path, method, parameters,
// the role each needs and how to authenticate. Bodies are JSON unless noted; the types are
// the ones in web/src/lib/api.ts.
func (s *Server) openAPI(routes []route) map[string]any {
	sorted := append([]route(nil), routes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].path != sorted[j].path {
			return sorted[i].path < sorted[j].path
		}
		return sorted[i].method < sorted[j].method
	})
	paths := map[string]map[string]any{}
	tags := map[string]bool{}
	for _, r := range sorted {
		if strings.HasPrefix(r.path, "/api/v1/cluster/peer/") || strings.HasPrefix(r.path, "/api/v1/migrate/") {
			continue // host-to-host protocol, not for people or scripts
		}
		p := pathParam.ReplaceAllString(r.path, "{$1}")
		op := map[string]any{
			"operationId": r.handler,
			"tags":        []string{tagOf(r.path)},
			"responses": map[string]any{
				"2XX": map[string]any{"description": "success"},
				"default": map[string]any{"description": "error", "content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}},
				}},
			},
		}
		tags[tagOf(r.path)] = true
		if sum := sentence(r.handler); sum != "" {
			op["summary"] = sum
		}
		role := requiredRole(r.method, p)
		if role == auth.NoAccess {
			op["security"] = []any{}
			op["description"] = "No sign-in needed."
		} else {
			op["description"] = "Needs role: " + role.String() + "."
		}
		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(r.path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if r.method == http.MethodPost || r.method == http.MethodPut || r.method == http.MethodPatch {
			body := map[string]any{"type": "object"}
			if strings.HasPrefix(p, "/api/v1/projects") {
				// Projects (ADR-0060): bodies are whole Project objects.
				body = map[string]any{"$ref": "#/components/schemas/Project"}
			}
			if strings.HasPrefix(p, "/api/v1/domains/") && strings.HasSuffix(p, "/project") {
				body = map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string", "description": "the project the machine moves into; empty makes it global again"}}}
			}
			op["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": body}}}
		}
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(r.method)] = op
	}
	var tagList []any
	names := make([]string, 0, len(tags))
	for t := range tags {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		tagList = append(tagList, map[string]any{"name": t})
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "VirtoMir API",
			"version": s.version,
			"description": "Everything the VirtoMir web UI does goes through this API. Authenticate with an API token " +
				"(Account → API tokens) as `Authorization: Bearer <token>`; its role is capped by its owner's. " +
				"In a cluster, /api/v1/hosts/{host}/… reaches the same endpoint on another member.",
		},
		"servers": []any{map[string]any{"url": "/"}},
		"tags":    tagList,
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"token":   map[string]any{"type": "http", "scheme": "bearer"},
				"session": map[string]any{"type": "apiKey", "in": "cookie", "name": auth.CookieName},
			},
			"schemas": map[string]any{
				"Error":             map[string]any{"type": "object", "properties": map[string]any{"error": map[string]any{"type": "string"}}},
				"Project":           projectSchema(),
				"complianceResult":  complianceResultSchema(),
				"ComplianceResult":  complianceResultSchema(),
				"hostCompliance":    hostComplianceSchema(),
				"HostCompliance":    hostComplianceSchema(),
				"fipsInfo":          fipsInfoSchema(),
				"FIPSInfo":          fipsInfoSchema(),
				"auditVerification": auditVerificationSchema(),
				"AuditVerification": auditVerificationSchema(),
			},
		},
		"security": []any{map[string]any{"token": []any{}}, map[string]any{"session": []any{}}},
	}
}
