package hub

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/fileserver"
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
	resolved, err := s.Files.Resolve(env, path)
	if errors.Is(err, fileserver.ErrOutsideRoot) {
		s.warn("a file request tried to leave the root",
			"node_id", nodeID, "env", env, "path", path)
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
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
