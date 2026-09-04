package builtin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A fetch returns the status, the headers and the body.
func TestHTTPQueryReturnsWhatTheServerSaid(t *testing.T) {
	r := New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Halite", "seen")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	got, err := r.Exec.Call(realCtx(t), "http.query", value.MapOf("url", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(*value.Map)
	if !ok {
		t.Fatalf("query returned %#v", got)
	}
	if status, _ := m.Get("status"); status != int64(201) {
		t.Errorf("status = %#v", status)
	}
	if text, _ := m.Get("text"); text != "hello" {
		t.Errorf("text = %#v", text)
	}
	headers, _ := m.Get("headers")
	hm, _ := headers.(*value.Map)
	if hm == nil {
		t.Fatalf("headers = %#v", headers)
	}
	if v, _ := hm.Get("X-Halite"); v != "seen" {
		t.Errorf("the response headers were not carried: %#v", v)
	}
}

// A JSON body is parsed into the data model, so a template can index it
// rather than parsing a string.
func TestHTTPQueryDecodesADocument(t *testing.T) {
	r := New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"version": "1.4.2", "tags": ["a", "b"]}`)
	}))
	defer server.Close()

	got, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "decode_type", "json"))
	if err != nil {
		t.Fatal(err)
	}
	m := got.(*value.Map)
	parsed, _ := m.Get("dict")
	doc, ok := parsed.(*value.Map)
	if !ok {
		t.Fatalf("dict = %#v", parsed)
	}
	if v, _ := doc.Get("version"); v != "1.4.2" {
		t.Errorf("version = %#v", v)
	}
	// And the text is still there, because a state may want both.
	if text, _ := m.Get("text"); !strings.Contains(text.(string), "1.4.2") {
		t.Errorf("text = %#v", text)
	}
}

// A body that will not parse is an error naming the URL, not a silent
// empty mapping.
func TestHTTPQueryRefusesADocumentItCannotRead(t *testing.T) {
	r := New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "{not: [valid")
	}))
	defer server.Close()

	_, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "decode_type", "json"))
	if err == nil {
		t.Fatal("a body that does not parse was accepted")
	}
	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("the error does not name the URL: %v", err)
	}
}

// The expected status is checked, because that is what makes the call
// usable in an `onlyif` without a template comparing numbers.
func TestHTTPQueryChecksTheStatusWhenAsked(t *testing.T) {
	r := New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "no such thing")
	}))
	defer server.Close()

	_, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "status", int64(200)))
	if err == nil {
		t.Fatal("a 404 was accepted where 200 was expected")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "200") {
		t.Errorf("the error does not carry both statuses: %v", err)
	}
	// The body is in the message, because it is usually the reason.
	if !strings.Contains(err.Error(), "no such thing") {
		t.Errorf("the error does not carry what the server said: %v", err)
	}

	// And a status that matches is not an error.
	if _, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "status", int64(404))); err != nil {
		t.Errorf("the expected status was reported as a failure: %v", err)
	}
}

// Headers, query parameters and basic authentication reach the server.
func TestHTTPQueryCarriesHeadersParamsAndCredentials(t *testing.T) {
	r := New()
	var seen struct {
		auth   string
		header string
		query  string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, _ := req.BasicAuth()
		seen.auth = user + ":" + pass
		seen.header = req.Header.Get("X-Token")
		seen.query = req.URL.RawQuery
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	_, err := r.Exec.Call(realCtx(t), "http.query", value.MapOf(
		"url", server.URL,
		"header_dict", value.MapOf("X-Token", "abc"),
		"params", value.MapOf("a", "1", "b", "two words"),
		"username", "ed", "password", "s3cret",
	))
	if err != nil {
		t.Fatal(err)
	}
	if seen.auth != "ed:s3cret" {
		t.Errorf("basic auth = %q", seen.auth)
	}
	if seen.header != "abc" {
		t.Errorf("header = %q", seen.header)
	}
	// Escaped, and ordered, so a template that builds the same call twice
	// produces the same URL.
	if seen.query != "a=1&b=two%20words" {
		t.Errorf("query = %q", seen.query)
	}
}

// The security contract, from the module's side. internal/safehttp has
// the detail; what is checked here is that a state reaches it.
func TestHTTPQueryRefusesTheMetadataService(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", "http://169.254.169.254/latest/meta-data/iam/"))
	if err == nil {
		t.Fatal("the metadata service was fetched")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the refusal does not say why it matters: %v", err)
	}
	if !strings.Contains(err.Error(), "allow_metadata") {
		t.Errorf("the refusal does not say how to permit it: %v", err)
	}
}

// `verify_ssl: False` is refused by name rather than accepted and
// quietly honoured either way.
//
// A state carrying it was written by somebody who wanted the check off.
// Accepting the argument and verifying anyway would lie to them;
// accepting it and turning verification off would break the contract
// SPEC 15.2 gives this module.
func TestHTTPQueryRefusesVerifySSLFalse(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", "https://example.com/", "verify_ssl", false))
	if err == nil {
		t.Fatal("verify_ssl: False was accepted")
	}
	if !strings.Contains(err.Error(), "verify_ssl") {
		t.Errorf("the refusal does not name the argument: %v", err)
	}
	if !strings.Contains(err.Error(), "trust store") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}

	// `verify_ssl: True` says the same thing this build already does, so
	// it is not an error — a tree carrying it migrates unchanged.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	if _, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "verify_ssl", true)); err != nil {
		t.Errorf("verify_ssl: True was refused: %v", err)
	}
}

// A scheme this module does not speak is refused with somewhere to go.
func TestHTTPQueryRefusesAFileURL(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(realCtx(t), "http.query", value.MapOf("url", "file:///etc/shadow"))
	if err == nil {
		t.Fatal("a file URL was fetched")
	}
	if !strings.Contains(err.Error(), "file.read") {
		t.Errorf("the refusal does not point at the module that reads files: %v", err)
	}
}

// A test run does not send a request that may change something at the
// other end, and says which method it declined to send.
//
// A GET still runs, because a --test that could not read is a --test
// that cannot evaluate an `onlyif`.
func TestHTTPQueryInTestModeSendsNoWrite(t *testing.T) {
	r := New()
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		methods = append(methods, req.Method)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	_, err := r.Exec.Call(testCtx(t), "http.query",
		value.MapOf("url", server.URL, "method", "POST", "data", "x=1"))
	if err == nil {
		t.Fatal("a POST was sent in test mode")
	}
	if !strings.Contains(err.Error(), "POST") {
		t.Errorf("the refusal does not name the method: %v", err)
	}

	if _, err := r.Exec.Call(testCtx(t), "http.query", value.MapOf("url", server.URL)); err != nil {
		t.Errorf("a GET was refused in test mode: %v", err)
	}
	if len(methods) != 1 || methods[0] != "GET" {
		t.Errorf("the server saw %v; only the GET should have arrived", methods)
	}
}

// A POST carries its body and its method.
func TestHTTPQuerySendsABody(t *testing.T) {
	r := New()
	var body, method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method = req.Method
		buf := make([]byte, req.ContentLength)
		_, _ = req.Body.Read(buf)
		body = string(buf)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	if _, err := r.Exec.Call(realCtx(t), "http.query",
		value.MapOf("url", server.URL, "method", "POST", "data", `{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if method != "POST" {
		t.Errorf("method = %q", method)
	}
	if body != `{"a":1}` {
		t.Errorf("body = %q", body)
	}
}
