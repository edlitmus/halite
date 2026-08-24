package api

import (
	"net/http"
	"strings"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// nodeList is `GET /v1/nodes`: every node with its grains, whether it
// is connected, and when it was last seen.
//
// Two calls to the hub rather than one per node: the cached grains for
// the whole estate, and the connected set. A hundred nodes should not
// be a hundred round trips.
func (s *Server) nodeList(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if !s.authorize(w, token, requestFor("cache.grains", "", nil, nil, true)) {
		return
	}
	cached, err := s.runner(r, "cache.grains", nil, nil)
	if err != nil {
		s.hubError(w, "reading the node cache", err)
		return
	}
	up, err := s.runner(r, "manage.up", nil, nil)
	if err != nil {
		s.hubError(w, "reading the connected set", err)
		return
	}

	connected := map[string]bool{}
	if list, ok := up.([]any); ok {
		for _, id := range list {
			connected[value.KeyString(id)] = true
		}
	}
	nodes, ok := cached.(*value.Map)
	if !ok {
		writeJSON(w, http.StatusOK, value.NewMap(0))
		return
	}
	out := value.NewMap(nodes.Len())
	for _, e := range nodes.Entries() {
		id := value.KeyString(e.Key)
		entry, ok := e.Val.(*value.Map)
		if !ok {
			continue
		}
		entry.Set("connected", connected[id])
		out.Set(id, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// nodeDetail is `GET /v1/nodes/{id}` and `POST /v1/nodes/{id}/state`.
func (s *Server) nodeDetail(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/nodes/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "that is not a node identifier")
		return
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		if !s.authorize(w, token, requestFor("cache.grains", "", []string{id}, nil, true)) {
			return
		}
		s.forwardRunner(w, r, "cache.grains", []string{id}, nil)

	case action == "state" && r.Method == http.MethodPost:
		s.applyState(w, r, token, id)

	default:
		writeError(w, http.StatusNotFound,
			"a node takes GET, and POST to its /state")
	}
}

// StateRequest is what `POST /v1/nodes/{id}/state` takes.
type StateRequest struct {
	// SLS are the states to apply. Empty is a highstate.
	SLS  []string `json:"sls,omitempty"`
	Env  string   `json:"saltenv,omitempty"`
	Test bool     `json:"test,omitempty"`
	// Timeout bounds the wait, as `/v1/run` does.
	Timeout string `json:"timeout,omitempty"`
}

// applyState runs state on one node.
//
// Targeted by list, not by glob: the node is in the path, and a path
// that was read as a pattern would let `/v1/nodes/*/state` apply to the
// estate through an endpoint that says it applies to one machine.
func (s *Server) applyState(w http.ResponseWriter, r *http.Request, token *apitoken.Token, id string) {
	var req StateRequest
	if r.ContentLength != 0 {
		if err := readJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if !s.authorize(w, token, requestFor("state.apply", id, req.SLS, nil, false)) {
		return
	}

	res, err := s.Hub.Submit(r.Context(), transport.SubmitRequest{
		Target:     id,
		TargetKind: "L",
		Fun:        "state.apply",
		Arg:        req.SLS,
		Env:        req.Env,
		Test:       req.Test,
		OnBehalfOf: token.Principal,
	})
	if err != nil {
		s.hubError(w, "submitting the state run", err)
		return
	}
	s.gather(w, r, res, req.Timeout)
}

// keys is `GET`, `POST`, and `DELETE` on `/v1/keys`: the enrollment
// management of SPEC 7.4, subject to RBAC like everything else.
func (s *Server) keys(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/keys")
	id = strings.TrimPrefix(id, "/")

	switch r.Method {
	case http.MethodGet:
		if !s.authorize(w, token, requestFor("key.list", "", nil, nil, true)) {
			return
		}
		s.forwardRunner(w, r, "key.list", nil, nil)

	case http.MethodPost:
		var req struct {
			Node   string `json:"node"`
			Action string `json:"action"`
			Reason string `json:"reason,omitempty"`
		}
		if err := readJSON(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Node == "" {
			writeError(w, http.StatusBadRequest, "a key decision names the node")
			return
		}
		fun := "key." + req.Action
		switch req.Action {
		case "accept", "reject", "revoke":
		default:
			writeError(w, http.StatusBadRequest,
				"a key decision is accept, reject, or revoke; deleting is DELETE")
			return
		}
		if !s.authorize(w, token, requestFor(fun, "", []string{req.Node}, nil, true)) {
			return
		}
		kwargs := map[string]any{}
		if req.Reason != "" {
			kwargs["reason"] = req.Reason
		}
		s.forwardRunner(w, r, fun, []string{req.Node}, kwargs)

	case http.MethodDelete:
		if id == "" {
			writeError(w, http.StatusBadRequest, "deleting a key names the node in the path")
			return
		}
		if !s.authorize(w, token, requestFor("key.delete", "", []string{id}, nil, true)) {
			return
		}
		s.forwardRunner(w, r, "key.delete", []string{id}, nil)

	default:
		writeError(w, http.StatusMethodNotAllowed, "keys take GET, POST, and DELETE")
	}
}

// OrchRequest is what `POST /v1/orch` takes.
type OrchRequest struct {
	SLS    string         `json:"sls"`
	Env    string         `json:"saltenv,omitempty"`
	Pillar map[string]any `json:"pillar,omitempty"`
	Test   bool           `json:"test,omitempty"`
}

// orchestrate is `POST /v1/orch`.
func (s *Server) orchestrate(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	var req OrchRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SLS == "" {
		writeError(w, http.StatusBadRequest, "an orchestration needs `sls`")
		return
	}
	if !s.authorize(w, token, requestFor("state.orchestrate", "", []string{req.SLS}, nil, true)) {
		return
	}

	kwargs := map[string]any{"sls": req.SLS}
	if req.Env != "" {
		kwargs["env"] = req.Env
	}
	if req.Test {
		kwargs["test"] = true
	}
	if len(req.Pillar) > 0 {
		kwargs["pillar"] = req.Pillar
	}
	s.forwardRunner(w, r, "state.orchestrate", nil, kwargs)
}

// orchDetail is `GET /v1/orch/{jid}`: the timeline.
func (s *Server) orchDetail(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	jid := strings.TrimPrefix(r.URL.Path, "/v1/orch/")
	if jid == "" || strings.Contains(jid, "/") {
		writeError(w, http.StatusBadRequest, "that is not an orchestration identifier")
		return
	}
	if !s.authorize(w, token, requestFor("state.orch_show", "", []string{jid}, nil, true)) {
		return
	}
	s.forwardRunner(w, r, "state.orch_show", []string{jid}, nil)
}

// pillar is `GET /v1/pillar/{id}`.
//
// Behind a distinct high-privilege permission, per SPEC 22.1: reading
// one node's compiled pillar is reading its secrets, and a role written
// to let someone restart a service must not carry it because the
// function list said `*`. The role has to name it.
func (s *Server) pillar(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/pillar/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "that is not a node identifier")
		return
	}
	req := requestFor("pillar.show_pillar", "", []string{id}, nil, true)
	req.NeverWildcard = true
	if !s.authorize(w, token, req) {
		return
	}
	s.forwardRunner(w, r, "pillar.show_pillar", []string{id}, nil)
}

// runner calls a hub runner and decodes the answer, for a handler that
// needs the value rather than the response.
func (s *Server) runner(r *http.Request, fun string, arg []string, kwarg map[string]any) (any, error) {
	res, err := s.Hub.Runner(r.Context(), transport.RunnerRequest{Fun: fun, Arg: arg, Kwarg: kwarg})
	if err != nil {
		return nil, err
	}
	if !res.Success {
		return nil, errRunner{res.Fun, res.Error}
	}
	return value.DecodeJSON(res.Return)
}

type errRunner struct{ fun, msg string }

func (e errRunner) Error() string { return e.fun + ": " + e.msg }
