package builtin

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/safehttp"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// http, SPEC section 15.2.
//
// One function, and the whole of the module is its contract:
//
//	`query`, with mandatory certificate verification, a default timeout,
//	a maximum response size, a redirect limit, and a denylist for
//	link-local and metadata addresses unless explicitly permitted.
//	Salt's `http.query` will happily fetch 169.254.169.254 on request
//	from a templated state.
//
// internal/safehttp keeps the contract and explains why the address
// check has to be in the dialer rather than on the URL. What is here is
// the calling convention: the arguments a tree already writes, and the
// two Salt options this build refuses rather than accepts.

func registerHTTP(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "http", Function: "query",
				Doc: "Fetch a URL, with certificate verification, a timeout, a size limit, " +
					"a redirect limit, and link-local and cloud metadata addresses refused.",
				Params: []signature.Param{
					req("url", signature.String, "The URL. http and https only."),
					choice("method", "GET", "The request method.",
						"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"),
					opt("data", signature.String, "", "The request body."),
					opt("header_dict", signature.Map, nil, "Request headers."),
					opt("params", signature.Map, nil, "Query parameters, added to the URL."),
					opt("username", signature.String, "", "For basic authentication."),
					opt("password", signature.String, "", "For basic authentication."),
					choice("decode_type", "", "Parse the body. Empty returns it as text.",
						"", "json", "yaml"),
					opt("status", signature.Int, int64(0),
						"The status the caller expects. Any other is an error. Zero accepts any."),
					opt("timeout", signature.Int, int64(0),
						"Seconds to allow. Zero is the default of 30."),
					opt("max_body", signature.Int, int64(0),
						"Bytes to allow in the response. Zero is the default of 10 MiB."),
					opt("allow_metadata", signature.Bool, false,
						"Permit link-local and cloud metadata addresses. Off by default: "+
							"a state that can build a URL can be made to build one that "+
							"returns this machine's credentials."),
					// Declared so they can be refused by name.
					//
					// A tree migrating from Salt writes these, and the
					// argument validator would otherwise answer "is not a
					// parameter of this function" — which reads as a typo
					// and sends the author looking for a spelling error.
					// Declaring them costs nothing and turns that into a
					// sentence saying what this build does instead.
					opt("verify_ssl", signature.Bool, true,
						"Salt's switch for turning certificate verification off. "+
							"True is what this build always does; False is refused, because "+
							"SPEC 15.2 makes verification mandatory."),
					opt("cert", signature.String, "",
						"Salt's client certificate. Not implemented: SPEC 15.2 names only "+
							"the server side of the contract."),
				},
				Returns: "a mapping with status, headers, text, and dict when decode_type is set",
				// It changes nothing on this node, but a POST changes
				// something somewhere. TestUnreliable rather than
				// TestNotApplicable, because a --test run must not send
				// one and cannot predict what it would have done.
				TestMode: signature.TestUnreliable,
				Section:  "15.2",
			},
			Fn: httpQuery,
		},
	)
}

// httpQuery runs one request.
func httpQuery(c *exec.Context, args *value.Map) (any, error) {
	url := states.Str(args, "url", "")
	if url == "" {
		return nil, fmt.Errorf("http.query needs a url")
	}
	if err := safehttp.CheckURL(url); err != nil {
		return nil, err
	}
	if err := refuseSaltOnlyOptions(args); err != nil {
		return nil, err
	}

	method := strings.ToUpper(states.Str(args, "method", "GET"))
	body := states.Str(args, "data", "")

	// A --test run makes no request that would change something
	// elsewhere. A GET or a HEAD is safe to run and is often the whole
	// point of an `onlyif`; anything else is not this node's to predict.
	if c.Test && method != "GET" && method != "HEAD" {
		return nil, fmt.Errorf(
			"http.query would send a %s to %s, which may change something at the other end; "+
				"a test run does not send one", method, url)
	}

	params, hasParams := args.Get("params")
	full, err := withParams(url, params, hasParams)
	if err != nil {
		return nil, err
	}

	opts := safehttp.Options{
		Timeout:       time.Duration(states.Int(args, "timeout", 0)) * time.Second,
		MaxBody:       states.Int(args, "max_body", 0),
		AllowMetadata: states.Bool(args, "allow_metadata", false),
	}

	request, err := http.NewRequestWithContext(c.Ctx, method, full, bodyReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}
	if err := applyHeaders(request, args); err != nil {
		return nil, err
	}
	if user := states.Str(args, "username", ""); user != "" {
		request.SetBasicAuth(user, states.Str(args, "password", ""))
	}

	response, err := safehttp.Client(opts).Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer response.Body.Close()

	raw, err := safehttp.Body(response.Body, opts.MaxBody)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}

	if want := states.Int(args, "status", 0); want != 0 && int64(response.StatusCode) != want {
		return nil, fmt.Errorf("%s answered %d and the state expects %d: %s",
			url, response.StatusCode, want, firstLineOf(string(raw)))
	}

	out := value.NewMap(4)
	out.Set("status", int64(response.StatusCode))
	out.Set("headers", headerMap(response.Header))
	out.Set("text", string(raw))

	switch states.Str(args, "decode_type", "") {
	case "json", "yaml":
		// One parser for both. SPEC 10.1's YAML is a superset of JSON,
		// and using it for each means a document that parses here parses
		// the same way it would in a pillar file — rather than a state
		// getting one answer from `decode_type: json` and another from
		// the renderer reading the same bytes.
		parsed, _, err := yaml.Parse(raw, yaml.DefaultOptions(url))
		if err != nil {
			return nil, fmt.Errorf("%s did not return a document this build can read: %w", url, err)
		}
		out.Set("dict", parsed)
	}
	return out, nil
}

// refuseSaltOnlyOptions names the two arguments a tree may carry that
// this build will not honour.
//
// Refused rather than ignored. `verify_ssl: False` is the one that
// matters: a state carrying it was written by somebody who wanted the
// certificate check off, and a build that accepted the argument and
// verified anyway would be lying to them — while one that accepted it
// and *did* turn verification off would break the contract SPEC 15.2
// gives this module. Saying so is the only honest option.
func refuseSaltOnlyOptions(args *value.Map) error {
	if !states.Bool(args, "verify_ssl", true) {
		return fmt.Errorf(
			"verify_ssl: False is not accepted; SPEC 15.2 makes certificate " +
				"verification mandatory for http.query. To trust a private authority, " +
				"add it to the system trust store, where every other program on the " +
				"machine can see the decision too")
	}
	if states.Str(args, "cert", "") != "" {
		return fmt.Errorf(
			"a client certificate is not implemented for http.query; " +
				"SPEC 15.2 names only the server side of the contract")
	}
	return nil
}

// withParams adds query parameters to a URL.
func withParams(raw string, params any, ok bool) (string, error) {
	if !ok || params == nil {
		return raw, nil
	}
	m, isMap := params.(*value.Map)
	if !isMap || m.Len() == 0 {
		return raw, nil
	}
	var pairs []string
	for _, e := range m.Entries() {
		pairs = append(pairs, queryEscape(fmt.Sprint(e.Key))+"="+queryEscape(fmt.Sprint(e.Val)))
	}
	sort.Strings(pairs)
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + strings.Join(pairs, "&"), nil
}

// applyHeaders puts the caller's headers on the request.
func applyHeaders(request *http.Request, args *value.Map) error {
	v, ok := args.Get("header_dict")
	if !ok || v == nil {
		return nil
	}
	m, isMap := v.(*value.Map)
	if !isMap {
		return fmt.Errorf("header_dict is a mapping of header names to values")
	}
	for _, e := range m.Entries() {
		// Set rather than Add: a mapping cannot express two values for
		// one name, so a caller writing one means one.
		request.Header.Set(fmt.Sprint(e.Key), fmt.Sprint(e.Val))
	}
	return nil
}

// headerMap renders response headers, joining a repeated one rather than
// dropping the second.
func headerMap(h http.Header) *value.Map {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	out := value.NewMap(len(names))
	for _, name := range names {
		out.Set(name, strings.Join(h[name], ", "))
	}
	return out
}

func queryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", ch))
		}
	}
	return b.String()
}

// bodyReader is the request body, or nothing.
//
// The return type is the interface rather than *strings.Reader, and that
// is the whole point of the function. A nil *strings.Reader assigned to
// an io.Reader is not a nil io.Reader: it is a non-nil interface holding
// a nil pointer, so http.NewRequest takes it for a real body and calls
// Len on it. A GET with no body panicked.
func bodyReader(s string) io.Reader {
	if s == "" {
		return nil
	}
	return strings.NewReader(s)
}
