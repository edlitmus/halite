package builtin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/safehttp"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// maxRemoteSource bounds a fetched source.
//
// This implementation reads a managed file's contents into memory: it
// has to, because reporting what changed means diffing the bytes it is
// about to write against the bytes already there. A local source is
// read the same way, so the limit is not new with remote fetching --
// what is new is that the other end of a remote fetch is not under the
// operator's control, and a URL that answers forever should fail rather
// than take the node's memory with it.
//
// Salt streams to a cache file and has no limit. See DIVERGENCE 5.104.
const maxRemoteSource int64 = 64 << 20

// remoteSchemes are the schemes Salt treats as remote, from
// `salt/utils/files.py`'s REMOTE_PROTOS. Only http and https are fetched
// here; the rest are named so that they fail saying so rather than being
// handed to the filesystem.
var remoteSchemes = map[string]bool{
	"http": true, "https": true,
	"ftp": false, "s3": false, "swift": false,
}

// remoteScheme reports the scheme of a source Salt would fetch over the
// network, and whether this build can fetch it.
//
// A Windows path is why this parses rather than splitting on ":". `C:\`
// has a one-letter scheme and is a local path everywhere it appears.
func remoteScheme(source string) (scheme string, remote, supported bool) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme == "" {
		return "", false, false
	}
	s := strings.ToLower(u.Scheme)
	ok, known := remoteSchemes[s]
	if !known {
		return s, false, false
	}
	return s, true, ok
}

// fetchRemoteSource retrieves an http(s) source.
//
// The credentials in the URL are moved into an Authorization header
// rather than left in the request line, which is what Salt does and
// what keeps them out of a proxy's access log. They are not in the
// message this returns either -- the redactor of DIVERGENCE 5.103
// would strip them at the sink, but the cheaper guarantee is not to put
// them in the text in the first place.
func fetchRemoteSource(c *exec.Context, args *value.Map, source string) ([]byte, error) {
	if err := safehttp.CheckURL(source); err != nil {
		return nil, err
	}
	u, err := url.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("source %q is not a URL: %w", redactedURL(source), err)
	}

	// Salt's rule, from `salt/modules/file.py`: a remote source whose
	// hash cannot be checked is refused rather than fetched. An
	// unverified download becomes the contents of a managed file, and a
	// state that cannot say what it is about to write should not write
	// it.
	if states.Str(args, "source_hash", "") == "" && !states.Bool(args, "skip_verify", false) {
		return nil, fmt.Errorf(
			"%s is a remote source and nothing here can verify it: "+
				"set source_hash, or set skip_verify to true to accept whatever it returns",
			redactedURL(source))
	}

	user := u.User
	u.User = nil
	safe := u.String()

	request, err := http.NewRequestWithContext(c.Ctx, http.MethodGet, safe, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", safe, err)
	}
	if user != nil {
		password, _ := user.Password()
		request.SetBasicAuth(user.Username(), password)
	}

	opts := safehttp.Options{MaxBody: maxRemoteSource}
	response, err := safehttp.Client(opts).Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", safe, err)
	}
	defer response.Body.Close()

	// The status is checked before the body is read, because an error
	// page is a perfectly valid body and writing one into a managed
	// file is how a node ends up with an HTML 404 in /etc.
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("%s answered %s", safe, response.Status)
	}

	body, err := safehttp.Body(response.Body, opts.MaxBody)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", safe, err)
	}
	return body, nil
}

// redactedURL removes the userinfo from a URL for a message. It is not
// the redactor -- that runs at the sink and catches what this misses --
// but a message built here has no reason to carry a credential at all.
func redactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("<redacted>")
	return u.String()
}
