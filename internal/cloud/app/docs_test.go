package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/openapi"
	"github.com/thekiharani/monitor/internal/cloud/probe"
)

func docsApp() *App {
	return &App{cfg: config.Config{APIOrigin: "http://api.example.com", DashboardOrigin: "http://app.example.com"}}
}

func TestDocsRedirectsToDashboardReference(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://api.example.com/docs", nil)
	w := httptest.NewRecorder()
	docsApp().docs(w, r)
	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308", w.Code)
	}
	if got := w.Header().Get("Location"); got != "http://app.example.com/docs" {
		t.Errorf("location = %q, want dashboard docs", got)
	}
}

func TestDocsSpecServesEmbeddedContract(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://api.example.com/docs/openapi.yaml", nil)
	w := httptest.NewRecorder()
	docsApp().docsSpec(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.HasPrefix(w.Body.String(), "openapi: 3.1.0") {
		t.Errorf("body is not the openapi document: %q", w.Body.String()[:min(40, w.Body.Len())])
	}
}

func TestDocsConfigServesPublicOrigins(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://api.example.com/docs/config.json", nil)
	w := httptest.NewRecorder()
	docsApp().docsConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{`"api_origin"`, `"app_origin"`, `"status_origin"`, `"dashboard_url"`} {
		if !strings.Contains(body, want) {
			t.Errorf("config does not contain %q: %s", want, body)
		}
	}
}

func TestDocsRejectsNonAPIHost(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{"page": docsApp().docs, "spec": docsApp().docsSpec} {
		r := httptest.NewRequest(http.MethodGet, "http://app.example.com/docs", nil)
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: status = %d, want 421", name, w.Code)
		}
	}
}

func TestDocsConfigServesDashboardOrigin(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://app.example.com/docs/config.json", nil)
	w := httptest.NewRecorder()
	docsApp().docsConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (config is public for the dashboard origin)", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, `"dashboard_url"`) {
		t.Errorf("config does not include dashboard_url: %s", body)
	}
}

func TestDocsConfigRejectsUnknownHost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://unknown.example.com/docs/config.json", nil)
	w := httptest.NewRecorder()
	docsApp().docsConfig(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421", w.Code)
	}
}

type route struct{ method, path string }

func (r route) String() string { return r.method + " " + r.path }

var pathWildcard = regexp.MustCompile(`\{[^}]*\}`)

func normalisedRoute(method, path string) route {
	return route{method: strings.ToUpper(method), path: pathWildcard.ReplaceAllString(path, "{}")}
}

func samplePath(path string) string { return pathWildcard.ReplaceAllString(path, "sample") }

func registeredRoutes(t *testing.T) []route {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	constants := map[string]string{"probe.ClaimPath": probe.ClaimPath, "probe.ResultsPath": probe.ResultsPath}
	var patternText func(ast.Expr) string
	patternText = func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.BasicLit:
			text, err := strconv.Unquote(v.Value)
			if err != nil {
				t.Fatalf("route literal %s: %v", v.Value, err)
			}
			return text
		case *ast.BinaryExpr:
			if v.Op == token.ADD {
				return patternText(v.X) + patternText(v.Y)
			}
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok {
				if text, known := constants[pkg.Name+"."+v.Sel.Name]; known {
					return text
				}
			}
		}
		t.Fatalf("app.go registers a route whose pattern this test cannot read: %T; teach registeredRoutes about it", e)
		return ""
	}
	var routes []route
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "routes" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "mux" {
				return true
			}
			method, path, found := strings.Cut(patternText(call.Args[0]), " ")
			if !found {
				method, path = "", method
			}
			routes = append(routes, route{method: method, path: path})
			return true
		})
	}
	if len(routes) < 100 {
		t.Fatalf("found only %d routes in (*App).routes; the scan has stopped seeing them", len(routes))
	}
	mux := testApp(nil).routes()
	for _, r := range routes {
		if r.method == "" {
			continue
		}
		_, pattern := mux.Handler(httptest.NewRequest(r.method, samplePath(r.path), nil))
		if pattern != r.String() {
			t.Errorf("app.go declares %q but the mux routes it to %q", r, pattern)
		}
	}
	return routes
}

type contractOperation struct {
	route
	host string
}

func serverURL(line string) (string, bool) {
	_, rest, found := strings.Cut(line, "url:")
	if !found {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest != "" && (rest[0] == '\'' || rest[0] == '"') {
		end := strings.IndexByte(rest[1:], rest[0])
		if end < 0 {
			return "", false
		}
		return rest[1 : end+1], true
	}
	if end := strings.IndexAny(rest, ",}"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest), true
}

func splitServer(raw string) (host, base string) {
	_, rest, _ := strings.Cut(raw, "://")
	host, base, _ = strings.Cut(rest, "/")
	if base != "" {
		base = "/" + strings.TrimSuffix(base, "/")
	}
	return host, base
}

func contractOperations(t *testing.T, name, spec string) []contractOperation {
	t.Helper()
	methods := []string{"get", "post", "put", "patch", "delete"}
	var defaultServer, pathKey, pathServer string
	section := ""
	inPathServers := false
	var operations []contractOperation
	for line := range strings.Lines(spec) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "" {
			continue
		}
		if indent == 0 {
			section = strings.TrimSuffix(trimmed, ":")
			continue
		}
		switch {
		case section == "servers" && defaultServer == "":
			if u, ok := serverURL(trimmed); ok {
				defaultServer = u
			}
		case section != "paths":
		case indent == 2 && strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":"):
			pathKey, pathServer, inPathServers = strings.TrimSuffix(trimmed, ":"), "", false
		case indent == 4 && strings.HasPrefix(trimmed, "servers:"):
			inPathServers = true
			if u, ok := serverURL(trimmed); ok {
				pathServer = u
			}
		case indent > 4 && inPathServers && pathServer == "":
			if u, ok := serverURL(trimmed); ok {
				pathServer = u
			}
		case indent == 4:
			inPathServers = false
			method := strings.TrimSuffix(trimmed, ":")
			if !slices.Contains(methods, method) || pathKey == "" {
				continue
			}
			server := defaultServer
			if pathServer != "" {
				server = pathServer
			}
			host, base := splitServer(server)
			operations = append(operations, contractOperation{route: normalisedRoute(method, base+pathKey), host: host})
		}
	}
	if defaultServer == "" || len(operations) < 20 {
		t.Fatalf("%s: read %d operations and server %q; the scan has stopped understanding the file", name, len(operations), defaultServer)
	}
	return operations
}

func assertRoutesDocumented(t *testing.T, routes []route, prefixes []string, name, spec string) {
	t.Helper()
	documented := map[route]bool{}
	for _, op := range contractOperations(t, name, spec) {
		documented[op.route] = true
	}
	checked := 0
	for _, r := range routes {
		if r.method == "" || !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(r.path, p) }) {
			continue
		}
		checked++
		if !documented[normalisedRoute(r.method, r.path)] {
			t.Errorf("%s is routed but has no operation in %s", r, name)
		}
	}
	if checked == 0 {
		t.Fatalf("no routes under %v were found to check", prefixes)
	}
}

func TestEveryBearerRouteIsInThePublicContract(t *testing.T) {
	assertRoutesDocumented(t, registeredRoutes(t), []string{"/api/v1/", "/ping/", "/api/probe/"}, "openapi.yaml", openapi.Spec)
}

func TestEveryControlRouteIsInTheControlContract(t *testing.T) {
	assertRoutesDocumented(t, registeredRoutes(t), []string{"/api/control/v1/"}, "control.openapi.yaml", openapi.ControlSpec)
}

func TestEveryContractOperationOnTheAppHostIsRouted(t *testing.T) {
	mux := testApp(nil).routes()
	for name, spec := range map[string]string{"openapi.yaml": openapi.Spec, "control.openapi.yaml": openapi.ControlSpec} {
		for _, op := range contractOperations(t, name, spec) {
			if op.host != "monitor.noria.co.ke" {
				continue
			}
			_, pattern := mux.Handler(httptest.NewRequest(op.method, samplePath(op.path), nil))
			method, path, _ := strings.Cut(pattern, " ")
			if normalisedRoute(method, path) != op.route {
				t.Errorf("%s documents %s but the mux routes it to %q", name, op.route, pattern)
			}
		}
	}
}
