package hub

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/transport"
)

// files serves SPEC 6.2's two file endpoints:
//
//	GET /v1/files/{env}          the manifest for an environment or subtree
//	GET /v1/files/{env}/{path}   one file
//
// Containment is enforced by fileserver.Roots on both, which is the
// point of routing them through the same resolver the node's local
// tree uses: Salt's CVE-2020-11652 was a traversal in exactly this code
// path, and having one implementation of the check means there is one
// place for it to be right.
func (s *Server) files(w http.ResponseWriter, r *http.Request, nodeID string) {
	// Wrapped for the whole handler, so a refusal is counted with the
	// code it was refused with: a metric that counts only what was
	// served answers the wrong half of "is the file server healthy".
	counted := &countingWriter{ResponseWriter: w, status: http.StatusOK}
	defer func() { s.countFileRequest(counted) }()
	w = counted

	if s.Files == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errors.New("this hub serves no files; set file_roots"))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, transport.PathFiles)
	env, path, _ := strings.Cut(rest, "/")
	if env == "" {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			errors.New("a file request names an environment: /v1/files/{env}/{path}"))
		return
	}
	if !s.servesEnv(env) {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused,
			fmt.Errorf("this hub serves no environment %q", env))
		return
	}
	if path == "" {
		s.manifest(w, r, env)
		return
	}
	s.file(w, r, nodeID, env, path)
}

func (s *Server) servesEnv(env string) bool {
	for _, e := range s.Files.Envs() {
		if e == env {
			return true
		}
	}
	return false
}

// manifest answers with the path list, hashes, and sizes.
func (s *Server) manifest(w http.ResponseWriter, r *http.Request, env string) {
	prefix := r.URL.Query().Get("prefix")
	m, err := s.Files.Manifest(env, prefix, s.HashType)
	if errors.Is(err, fileserver.ErrOutsideRoot) {
		s.warn("a file listing tried to leave the root", "env", env, "prefix", prefix)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	transport.WriteJSON(w, http.StatusOK, m)
}

// file answers with one file's contents.
//
// http.ServeContent does the conditional request and the Range
// handling, which SPEC 13.5 asks for and which is a great deal of
// fiddly code to get wrong by hand.
func (s *Server) file(w http.ResponseWriter, r *http.Request, nodeID, env, path string) {
	// The hub's half of SPEC 26.3's file-transfer span, continuing the
	// node's trace through the `traceparent` its client sent.
	//
	// It is the other end of the node's span rather than a second span
	// for the same thing: what it separates is the time the hub spent
	// resolving, hashing and writing the file from the time the transfer
	// took, and those are two different problems with two different
	// answers. A request with no `traceparent` -- an untraced node, or a
	// node older than this -- produces no span at all rather than a
	// root, because a file transfer with no job above it is a fragment
	// nobody can use.
	var span *tracing.Span
	if parent := tracing.Extract(r.Header); parent.IsValid() {
		span = s.Tracer.StartSpan(parent, "file serve", tracing.KindServer)
		defer span.End()
		span.SetAttr("halite.file.env", env)
		span.SetAttr("halite.file.uri", path)
		span.SetAttr("halite.node", nodeID)
	}

	resolved, err := s.Files.Resolve(env, path)
	if errors.Is(err, fileserver.ErrOutsideRoot) {
		s.warn("a file request tried to leave the root",
			"node_id", nodeID, "env", env, "path", path)
		span.Fail(err)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, err)
		return
	}
	if err != nil {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused,
			fmt.Errorf("%s is not served from the %q environment", path, env))
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused,
			fmt.Errorf("%s is not served from the %q environment", path, env))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		transport.WriteError(w, http.StatusNotFound, transport.CodeRefused,
			fmt.Errorf("%s is not a file", path))
		return
	}

	// The digest is the entity tag, so a node that already has the file
	// gets a 304 and no body. Modification time would be wrong here: a
	// tree redeployed from git has new timestamps and identical
	// contents, and re-sending the estate's whole tree on every deploy
	// is what makes Salt's file server the bottleneck it is.
	algorithm, digest, err := s.Files.HashOf(env, path, s.HashType)
	if err == nil {
		w.Header().Set("ETag", `"`+algorithm+":"+digest+`"`)
		w.Header().Set("X-Halite-Hash", algorithm+":"+digest)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	span.SetAttr("halite.file.bytes", info.Size())
	// Whether the node already had it. This is the attribute the node's
	// own span cannot carry -- it sits below that seam -- and it is the
	// one that answers "is this tree being re-sent on every run".
	span.SetAttr("halite.file.not_modified", matchesETag(r, algorithm, digest))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// matchesETag reports whether the request already carries the digest
// this file has, which is what `http.ServeContent` is about to turn
// into a 304.
//
// Read here rather than inferred from the status, because
// `http.ServeContent` writes the response itself and the handler never
// sees what it decided.
func matchesETag(r *http.Request, algorithm, digest string) bool {
	if algorithm == "" || digest == "" {
		return false
	}
	return strings.Contains(r.Header.Get("If-None-Match"), algorithm+":"+digest)
}
