package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// Hook is one configured webhook path, per SPEC 22.2.
//
// Every field that could be omitted to produce an unauthenticated hook
// is required by the loader instead. Salt's webhook can be configured
// without authentication and is a well-known weak point because of it;
// there is no configuration here that produces one.
type Hook struct {
	// Path is the suffix under /v1/hook/.
	Path string
	// Auth is `hmac`, `token`, or `mtls`.
	Auth string
	// Secret is the shared secret for `hmac`, or the expected bearer
	// value for `token`.
	Secret string
	// SignatureHeader carries the HMAC, default X-Halite-Signature.
	SignatureHeader string
	// TimestampHeader carries the replay window's timestamp.
	TimestampHeader string
	// ReplayWindow is how far out a timestamp may be.
	ReplayWindow time.Duration
	// ContentTypes is the allowlist. Empty means application/json.
	ContentTypes []string
	// MaxBody bounds the request before it is parsed.
	MaxBody int64
	// RateLimit is requests per second for this path, with RateBurst
	// allowed at once.
	RateLimit float64
	RateBurst int
	// Principal is who a delivery through this hook is recorded as, so
	// a reactor authorizes on the principal rather than on the payload.
	Principal string
}

// The defaults SPEC 22.2 leaves to the implementation.
const (
	DefaultSignatureHeader = "X-Halite-Signature"
	DefaultTimestampHeader = "X-Halite-Timestamp"
	DefaultReplayWindow    = 5 * time.Minute
	DefaultHookBody        = 1 << 20
	DefaultHookRate        = 10
)

// Hooks is the configured set, with the replay cache and the rate
// limiters that go with it.
type Hooks struct {
	byPath map[string]*Hook

	mu sync.Mutex
	// seen is the nonce cache: a signature already accepted inside the
	// replay window is a replay, not a delivery.
	seen map[string]time.Time
	// tokens and filled are the per-path buckets.
	tokens map[string]float64
	filled map[string]time.Time
}

// NewHooks prepares a set.
func NewHooks(hooks []*Hook) *Hooks {
	h := &Hooks{
		byPath: map[string]*Hook{},
		seen:   map[string]time.Time{},
		tokens: map[string]float64{},
		filled: map[string]time.Time{},
	}
	for _, hook := range hooks {
		h.byPath[hook.Path] = hook
	}
	return h
}

// Len is how many paths are configured.
func (h *Hooks) Len() int {
	if h == nil {
		return 0
	}
	return len(h.byPath)
}

// ParseHooks reads the `hooks` configuration.
func ParseHooks(v any) ([]*Hook, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`hooks` is a mapping of path to its settings")
	}

	var out []*Hook
	for _, e := range m.Entries() {
		path := strings.Trim(value.KeyString(e.Key), "/")
		if path == "" {
			return nil, fmt.Errorf("a hook needs a path")
		}
		body, ok := e.Val.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("hook %s: the settings are a mapping", path)
		}
		hook := &Hook{
			Path:            path,
			SignatureHeader: DefaultSignatureHeader,
			TimestampHeader: DefaultTimestampHeader,
			ReplayWindow:    DefaultReplayWindow,
			MaxBody:         DefaultHookBody,
			RateLimit:       DefaultHookRate,
			Principal:       "hook:" + path,
		}
		for _, f := range body.Entries() {
			key := value.KeyString(f.Key)
			switch key {
			case "auth":
				hook.Auth = value.KeyString(f.Val)
			case "secret":
				hook.Secret = value.KeyString(f.Val)
			case "signature_header":
				hook.SignatureHeader = value.KeyString(f.Val)
			case "timestamp_header":
				hook.TimestampHeader = value.KeyString(f.Val)
			case "replay_window":
				d, err := time.ParseDuration(value.KeyString(f.Val))
				if err != nil {
					return nil, fmt.Errorf("hook %s: replay_window: %w", path, err)
				}
				hook.ReplayWindow = d
			case "content_types":
				list, ok := f.Val.([]any)
				if !ok {
					return nil, fmt.Errorf("hook %s: `content_types` is a list", path)
				}
				for _, c := range list {
					hook.ContentTypes = append(hook.ContentTypes, value.KeyString(c))
				}
			case "max_body":
				n, ok := f.Val.(int64)
				if !ok {
					return nil, fmt.Errorf("hook %s: `max_body` is a number of bytes", path)
				}
				hook.MaxBody = n
			case "rate_limit":
				n, err := asRate(f.Val)
				if err != nil {
					return nil, fmt.Errorf("hook %s: rate_limit: %w", path, err)
				}
				hook.RateLimit = n
			case "principal":
				hook.Principal = value.KeyString(f.Val)
			default:
				return nil, fmt.Errorf("hook %s: %q is not a hook setting", path, key)
			}
		}
		// The check that makes SPEC 22.2's promise true: there is no
		// value of `auth` that means none, and omitting it is an error
		// rather than a default.
		switch hook.Auth {
		case "hmac", "token":
			if hook.Secret == "" {
				return nil, fmt.Errorf("hook %s: `%s` needs a `secret`", path, hook.Auth)
			}
		case "mtls":
		case "":
			return nil, fmt.Errorf(
				"hook %s names no `auth`; every hook declares one, and there is no "+
					"unauthenticated hook (SPEC 22.2)", path)
		default:
			return nil, fmt.Errorf(
				"hook %s: `auth: %s` is not a method; there are hmac, token, and mtls",
				path, hook.Auth)
		}
		if hook.RateBurst == 0 {
			hook.RateBurst = int(hook.RateLimit)
			if hook.RateBurst < 1 {
				hook.RateBurst = 1
			}
		}
		out = append(out, hook)
	}
	return out, nil
}

func asRate(v any) (float64, error) {
	switch t := v.(type) {
	case int64:
		return float64(t), nil
	case float64:
		return t, nil
	}
	return 0, fmt.Errorf("%s is not a number of requests per second", value.TypeName(v))
}

// hooks is the configured set, or an empty one so that a service with
// none answers a delivery the way it answers any unconfigured path.
func (s *Server) hooks() *Hooks {
	if s.Hooks == nil {
		return NewHooks(nil)
	}
	return s.Hooks
}

// hook is `POST /v1/hook/{path}`.
//
// The order is the point: the raw body is read under a limit, the
// signature is verified against those bytes, and only then is anything
// parsed. Verifying a re-serialised body would be verifying something
// the sender never signed.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/hook/"), "/")
	cfg, ok := s.hooks().byPath[path]
	if !ok {
		// The same answer as an unconfigured path anywhere else: a
		// hook endpoint that distinguished "no such hook" from "wrong
		// signature" would enumerate the hooks.
		writeError(w, http.StatusNotFound, "no such hook")
		return
	}

	if !s.hooks().allow(cfg, s.now()) {
		writeError(w, http.StatusTooManyRequests, "too many deliveries to this hook")
		return
	}
	if err := checkContentType(cfg, r.Header.Get("Content-Type")); err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.MaxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "the body is larger than this hook accepts")
		return
	}
	nonce, err := s.authenticateHook(cfg, r, body)
	if err != nil {
		s.warn("hook delivery refused",
			"hook", path, "remote", remoteHost(r), "error", err.Error())
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	namePrincipal(w, cfg.Principal)

	// The tag carries the path, and the payload carries the principal
	// the delivery authenticated as -- so a reactor authorizes on who
	// sent it rather than on what the body claims.
	tag := "halite/hook/" + path
	payload := map[string]any{
		"_principal": cfg.Principal,
		"_remote":    remoteHost(r),
	}
	// The body is carried as what it parsed to when it is JSON, and as
	// the text otherwise: a reaction reading `data['body']['version']`
	// should not have to parse a string first, and a delivery that is
	// not JSON should still reach the bus rather than being dropped.
	if decoded, err := value.DecodeJSON(body); err == nil {
		payload["body"] = decoded
	} else {
		payload["body"] = string(body)
	}

	res, err := s.Hub.Runner(r.Context(), transport.RunnerRequest{
		Fun:   "event.send",
		Kwarg: map[string]any{"tag": tag, "data": payload},
	})
	if err != nil {
		s.hubError(w, "putting the delivery on the bus", err)
		return
	}
	if !res.Success {
		writeError(w, http.StatusBadGateway, res.Error)
		return
	}

	// The nonce is recorded here, after the delivery landed, and not
	// when the signature verified.
	//
	// Recording it earlier is strictly safer against a replay and costs
	// more than it saves: a delivery that fails downstream is one the
	// sender will retry, carrying the same signature. Refusing that as
	// a replay turns a transient fault into a lost event, which is the
	// failure a webhook exists to avoid.
	if nonce != "" {
		s.hooks().remember(nonce, s.now())
	}
	s.info("hook delivered", "hook", path, "principal", cfg.Principal, "tag", tag)
	writeJSON(w, http.StatusAccepted, map[string]any{"tag": tag})
}

// authenticateHook applies the method the path declared, returning the
// nonce to record once the delivery has landed.
func (s *Server) authenticateHook(cfg *Hook, r *http.Request, body []byte) (string, error) {
	switch cfg.Auth {
	case "token":
		presented, ok := bearer(r)
		if !ok {
			return "", fmt.Errorf("this hook needs a bearer token")
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(cfg.Secret)) != 1 {
			return "", fmt.Errorf("that token was not accepted")
		}
		return "", nil

	case "mtls":
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			return "", fmt.Errorf("this hook needs a client certificate")
		}
		return "", nil

	case "hmac":
		return s.verifySignature(cfg, r, body)
	}
	return "", fmt.Errorf("this hook declares no authentication method")
}

// verifySignature checks the HMAC over the raw body, with the replay
// window and the nonce cache of SPEC 22.2.
func (s *Server) verifySignature(cfg *Hook, r *http.Request, body []byte) (string, error) {
	presented := r.Header.Get(cfg.SignatureHeader)
	if presented == "" {
		return "", fmt.Errorf("this hook needs a %s header", cfg.SignatureHeader)
	}
	presented = strings.TrimPrefix(presented, "sha256=")

	stamp := r.Header.Get(cfg.TimestampHeader)
	if stamp == "" {
		return "", fmt.Errorf("this hook needs a %s header", cfg.TimestampHeader)
	}
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return "", fmt.Errorf("the %s header is not an RFC 3339 timestamp", cfg.TimestampHeader)
	}
	now := s.now()
	if drift := now.Sub(when); drift > cfg.ReplayWindow || drift < -cfg.ReplayWindow {
		return "", fmt.Errorf("the delivery is outside this hook's replay window of %s", cfg.ReplayWindow)
	}

	// The timestamp is signed with the body, or a captured signature
	// could be replayed with a fresh timestamp.
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	mac.Write([]byte(stamp))
	mac.Write([]byte("."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(presented), []byte(want)) != 1 {
		return "", fmt.Errorf("the signature did not verify")
	}
	nonce := cfg.Path + "\x00" + presented
	if s.hooks().alreadySeen(nonce, now, cfg.ReplayWindow) {
		// Verified and already delivered: a replay inside the window,
		// which the signature alone cannot tell from the original.
		return "", fmt.Errorf("this delivery has already been accepted")
	}
	return nonce, nil
}

// checkContentType applies the allowlist.
func checkContentType(cfg *Hook, header string) error {
	got := strings.TrimSpace(strings.Split(header, ";")[0])
	allowed := cfg.ContentTypes
	if len(allowed) == 0 {
		allowed = []string{"application/json"}
	}
	for _, want := range allowed {
		if strings.EqualFold(got, want) {
			return nil
		}
	}
	return fmt.Errorf("this hook accepts %s", strings.Join(allowed, ", "))
}

// alreadySeen reports whether a signature has already been delivered
// inside the window.
func (h *Hooks) alreadySeen(key string, now time.Time, window time.Duration) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, at := range h.seen {
		if now.Sub(at) > window {
			delete(h.seen, k)
		}
	}
	_, already := h.seen[key]
	return already
}

// remember records a delivery that landed, so the next one carrying the
// same signature is a replay.
func (h *Hooks) remember(key string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen[key] = now
}

// allow is the per-path token bucket.
func (h *Hooks) allow(cfg *Hook, now time.Time) bool {
	if cfg.RateLimit <= 0 {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	burst := float64(cfg.RateBurst)
	filled, seen := h.filled[cfg.Path]
	if !seen {
		h.tokens[cfg.Path], filled = burst, now
	}
	h.tokens[cfg.Path] += now.Sub(filled).Seconds() * cfg.RateLimit
	if h.tokens[cfg.Path] > burst {
		h.tokens[cfg.Path] = burst
	}
	h.filled[cfg.Path] = now
	if h.tokens[cfg.Path] < 1 {
		return false
	}
	h.tokens[cfg.Path]--
	return true
}
