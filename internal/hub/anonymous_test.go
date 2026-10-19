package hub

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// What a caller with no certificate reaches on the hub's port.
//
// SPEC 25.1 said the hub port had "no pre-auth application parsing", and
// that mutual TLS was "terminated by the standard library before any
// application code"; 25.2, the handbook and three comments said
// `/v1/health` was the only thing reachable without a certificate. None
// of it was true. The listener asks for a certificate and does not
// require one (VerifyClientCertIfGiven), because enrollment is how a
// node gets one; each route then decides. So the router runs for every
// anonymous request, and `POST /v1/enroll` reads a JSON body, parses a
// CSR, checks a token and may write a pending record, all for a caller
// nobody has authenticated (DIVERGENCE 5.399).
//
// The claim now made is the narrower one this test holds: which routes
// an anonymous caller reaches, and that every other route refuses it
// before its handler runs. The routes are read out of Handler's source,
// not listed here, so a route added without a certificate check fails
// this test rather than joining the list unnoticed.

// anonymousRoutes are the handlers mounted without s.authenticated or
// s.operator in front of them, and why.
var anonymousRoutes = map[string]string{
	"health":   "liveness, for a load balancer that holds no certificate",
	"enroll":   "how a node without a certificate gets one (SPEC 7.3)",
	"notFound": "the answer for a path that is not a route",
}

type mountedRoute struct {
	method, path string
	// guard is "authenticated", "operator", or "" for a handler mounted
	// bare; handler is the method it reaches.
	guard, handler string
}

// mountedRoutes reads every mux.HandleFunc in Handler, resolving the
// transport.Path constants from wire.go.
func mountedRoutes(t *testing.T) []mountedRoute {
	t.Helper()
	fset := token.NewFileSet()
	wire, err := parser.ParseFile(fset, "../transport/wire.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	ast.Inspect(wire, func(n ast.Node) bool {
		if spec, ok := n.(*ast.ValueSpec); ok {
			for i, name := range spec.Names {
				if i >= len(spec.Values) {
					continue
				}
				if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					paths[name.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
		return true
	})

	src, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var routes []mountedRoute
	for _, decl := range src.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Handler" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			pattern := patternOf(t, fset, call.Args[0], paths)
			r := mountedRoute{method: "GET", path: pattern}
			if method, path, ok := strings.Cut(pattern, " "); ok {
				r.method, r.path = method, path
			}
			switch h := call.Args[1].(type) {
			case *ast.SelectorExpr:
				r.handler = h.Sel.Name
			case *ast.CallExpr:
				guard, ok := h.Fun.(*ast.SelectorExpr)
				inner, ok2 := h.Args[0].(*ast.SelectorExpr)
				if !ok || !ok2 || len(h.Args) != 1 {
					t.Fatalf("%s: a handler this test cannot read", fset.Position(h.Pos()))
				}
				r.guard, r.handler = guard.Sel.Name, inner.Sel.Name
			default:
				t.Fatalf("%s: a handler this test cannot read", fset.Position(call.Args[1].Pos()))
			}
			routes = append(routes, r)
			return true
		})
	}
	if len(routes) < 10 {
		t.Fatalf("read %d routes out of Handler; the parse has stopped finding them", len(routes))
	}
	return routes
}

// patternOf evaluates a pattern written as string literals and
// transport.PathX constants joined by +.
func patternOf(t *testing.T, fset *token.FileSet, e ast.Expr, paths map[string]string) string {
	t.Helper()
	switch e := e.(type) {
	case *ast.BasicLit:
		s, _ := strconv.Unquote(e.Value)
		return s
	case *ast.SelectorExpr:
		if v, ok := paths[e.Sel.Name]; ok {
			return v
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return patternOf(t, fset, e.X, paths) + patternOf(t, fset, e.Y, paths)
		}
	}
	t.Fatalf("%s: a route pattern this test cannot read", fset.Position(e.Pos()))
	return ""
}

func TestOnlyTheNamedRoutesTakeACallerWithoutACertificate(t *testing.T) {
	routes := mountedRoutes(t)
	var bare []string
	for _, r := range routes {
		if r.guard == "" {
			bare = append(bare, r.handler)
			if _, ok := anonymousRoutes[r.handler]; !ok {
				t.Errorf("%s %s reaches %s with no certificate check in front of it; "+
					"wrap it in s.authenticated or s.operator, or, if it must take an "+
					"anonymous caller, add it to anonymousRoutes and to SPEC 25.1",
					r.method, r.path, r.handler)
			}
		} else if r.guard != "authenticated" && r.guard != "operator" {
			t.Errorf("%s %s is behind %s, which this test does not know checks a certificate",
				r.method, r.path, r.guard)
		}
	}
	for name := range anonymousRoutes {
		if !slices.Contains(bare, name) {
			t.Errorf("anonymousRoutes names %s, which Handler no longer mounts bare", name)
		}
	}
}

// Over the wire, as an unenrolled node connects: every route, with no
// certificate. The guarded ones refuse before their handler runs; health
// answers; enroll reads the body and parses the CSR in it, which is the
// application parsing SPEC 25.1 used to say did not happen.
func TestACallerWithoutACertificateOverTheWire(t *testing.T) {
	l := newLab(t)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   transport.EnrollConfig(l.ca.Cert, "127.0.0.1"),
		ForceAttemptHTTP2: true,
	}}
	t.Cleanup(client.CloseIdleConnections)

	ask := func(method, path, body string) (int, transport.Error, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, l.url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var e transport.Error
		_ = json.Unmarshal(raw, &e)
		return resp.StatusCode, e, string(raw)
	}

	for _, r := range mountedRoutes(t) {
		path := strings.NewReplacer("{jid}", "20261019120000000000", "{path...}", "base/top.sls").Replace(r.path)
		status, e, raw := ask(r.method, path, "{}")
		switch r.handler {
		case "health":
			if status != http.StatusOK || !strings.HasPrefix(raw, "halite-hub ") {
				t.Errorf("health: %d %q", status, raw)
			}
		case "enroll":
			// Covered below, with a body worth parsing.
		case "notFound":
			if status, _, _ := ask("GET", "/v1/no-such-route", ""); status != http.StatusNotFound {
				t.Errorf("an unrouted path: %d", status)
			}
		default:
			if status != http.StatusUnauthorized || e.Code != transport.CodeRefused {
				t.Errorf("%s %s (%s) without a certificate: %d %s", r.method, path, r.handler, status, raw)
			}
		}
	}

	// A body that is JSON and a CSR that is PEM but not a CSR. The answer
	// comes from the CSR parser, which is application code deciding what
	// to do with an anonymous caller's bytes.
	csr := "-----BEGIN CERTIFICATE REQUEST-----\nAAAA\n-----END CERTIFICATE REQUEST-----\n"
	body, _ := json.Marshal(transport.EnrollRequest{CSR: csr})
	status, e, raw := ask("POST", transport.PathEnroll, string(body))
	if status != http.StatusBadRequest || e.Code != transport.CodeMalformed {
		t.Errorf("enroll with a broken CSR: %d %s", status, raw)
	}
	// The parser's own words reach the caller: what it answered on
	// go1.27.2 was "asn1: structure error: tags don't match ...
	// certificateRequest @2".
	if !strings.Contains(e.Error, "asn1:") {
		t.Errorf("enroll's refusal is not the CSR parser's: %q", e.Error)
	}
}
