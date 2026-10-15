package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/ratelimit"
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
	// Auth is `hmac` or `token`. `mtls` is refused when the
	// configuration loads; see ParseHooks.
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
	// RateLimit is authenticated deliveries per second for this path,
	// with RateBurst allowed at once. Refusals are counted against a
	// bucket of their own at the same rate; see Hooks.
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
	// inFlight holds the signatures being delivered right now. A
	// signature is reserved here, under mu, in the same step that checks
	// seen, so two copies of one delivery arriving together cannot both
	// pass the check before either is recorded. DIVERGENCE 5.290.
	inFlight map[string]bool
	// buckets are the per-path rate limits on authenticated deliveries.
	buckets map[string]*ratelimit.Bucket
	// refusals are the per-path buckets for deliveries that did not
	// authenticate, or were replays. They are separate so that a caller
	// who holds no secret cannot spend the sender's allowance, which is
	// what one shared bucket in front of authentication allowed: ten
	// anonymous requests a second and the real sender got 429s.
	refusals map[string]*ratelimit.Bucket
}

// NewHooks prepares a set.
func NewHooks(hooks []*Hook) *Hooks {
	h := &Hooks{
		byPath:   map[string]*Hook{},
		seen:     map[string]time.Time{},
		inFlight: map[string]bool{},
		buckets:  map[string]*ratelimit.Bucket{},
		refusals: map[string]*ratelimit.Bucket{},
	}
	for _, hook := range hooks {
		h.byPath[hook.Path] = hook
	}
	return h
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
			case "secret_file":
				secret, err := config.ReadSecretFile(value.KeyString(f.Val))
				if err != nil {
					return nil, fmt.Errorf("hook %s: %w", path, err)
				}
				hook.Secret = secret
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
			// Refused rather than loaded, because it could never work and
			// read as though it did.
			//
			// The API's listener does not ask for a client certificate --
			// operators authenticate with tokens, and asking every
			// connection for a certificate is not something a hook path
			// can do on its own, because the handshake is over before the
			// path is known. So no delivery to an `mtls` hook ever carried
			// one and every delivery was refused. Asking for certificates
			// would not have fixed it either: the check was only that one
			// was presented, with no CA to verify it against and no
			// identity to match, so any certificate at all would have
			// passed. A setting that is either dead or open is worse than
			// one that is refused, because somebody reading the
			// configuration believes it. DIVERGENCE 5.290.
			return nil, fmt.Errorf(
				"hook %s: `auth: mtls` is not supported by this build: the API does not "+
					"request or verify client certificates, so no delivery could authenticate. "+
					"Use `hmac` or `token` (SPEC 22.2, DIVERGENCE 5.290)", path)
		case "":
			return nil, fmt.Errorf(
				"hook %s names no `auth`; every hook declares one, and there is no "+
					"unauthenticated hook (SPEC 22.2)", path)
		default:
			return nil, fmt.Errorf(
				"hook %s: `auth: %s` is not a method; there are hmac and token",
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

	if err := checkContentType(cfg, r.Header.Get("Content-Type")); err != nil {
		s.m().hookDeliveries.With(path, "wrong_content_type").Inc()
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.MaxBody))
	if err != nil {
		s.m().hookDeliveries.With(path, "too_large").Inc()
		writeError(w, http.StatusRequestEntityTooLarge, "the body is larger than this hook accepts")
		return
	}

	// Authentication comes before the rate limit, and the order is the
	// fix, not a detail. The limit used to be taken first, from one
	// bucket per path, so a caller with no secret at all could spend
	// the whole allowance and the real sender's deliveries were turned
	// away with 429. Now only a delivery that authenticated is counted
	// against the hook's limit, and a refusal is counted against a
	// bucket of its own -- which bounds what refusals cost in log lines,
	// not in work: each one is still read and checked, because nothing
	// short of checking tells a forgery from the sender. DIVERGENCE
	// 5.290.
	nonce, err := s.authenticateHook(cfg, r, body)
	if err != nil {
		s.refuseHook(w, r, cfg, "unauthenticated", http.StatusUnauthorized, err)
		return
	}

	// The signature is reserved before anything is delivered, in one
	// step with the check that it has not been delivered already. The
	// check and the record used to sit either side of the call to the
	// hub, so two copies of one signed delivery arriving together both
	// passed the check and both reached the bus.
	release, err := s.hooks().reserve(nonce, s.now(), cfg.ReplayWindow)
	if err != nil {
		status, result := http.StatusUnauthorized, "replayed"
		if errors.Is(err, errDeliveryInFlight) {
			status, result = http.StatusConflict, "in_flight"
		}
		s.refuseHook(w, r, cfg, result, status, err)
		return
	}
	// Released unless the delivery lands, on every path out of here --
	// including the rate limit below and a panic -- so a delivery that
	// did not reach the bus is one the sender can retry.
	landed := false
	defer func() {
		if !landed {
			release()
		}
	}()

	if !s.hooks().allow(cfg, s.now()) {
		s.m().hookDeliveries.With(path, "rate_limited").Inc()
		writeError(w, http.StatusTooManyRequests, "too many deliveries to this hook")
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
		s.m().hookDeliveries.With(path, "hub_unreachable").Inc()
		s.hubError(w, "putting the delivery on the bus", err)
		return
	}
	if !res.Success {
		s.m().hookDeliveries.With(path, "hub_refused").Inc()
		writeError(w, http.StatusBadGateway, res.Error)
		return
	}

	// The nonce is recorded as delivered here, after the delivery
	// landed, and not when the signature verified.
	//
	// Recording it earlier is strictly safer against a replay and costs
	// more than it saves: a delivery that fails downstream is one the
	// sender will retry, carrying the same signature. Refusing that as
	// a replay turns a transient fault into a lost event, which is the
	// failure a webhook exists to avoid. The reservation above keeps
	// that property -- it is released when the delivery fails -- while
	// closing the window in which two copies were both unrecorded.
	s.hooks().landed(nonce, s.now())
	landed = true
	s.m().hookDeliveries.With(path, "delivered").Inc()
	s.info("hook delivered", "hook", path, "principal", cfg.Principal, "tag", tag)
	writeJSON(w, http.StatusAccepted, map[string]any{"tag": tag})
}

// refuseHook answers a delivery that is not going to the bus because of
// who sent it or because it was sent before. It is logged while the
// hook's refusal bucket has room, and counted always: a caller holding
// no secret can make as many of these as it likes, and a warning per
// request would let it fill the log.
func (s *Server) refuseHook(w http.ResponseWriter, r *http.Request, cfg *Hook, result string, status int, err error) {
	s.m().hookDeliveries.With(cfg.Path, result).Inc()
	if s.hooks().allowRefusal(cfg, s.now()) {
		s.warn("hook delivery refused",
			"hook", cfg.Path, "remote", remoteHost(r), "result", result, "error", err.Error())
	}
	writeError(w, status, err.Error())
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
	// Whether it has been delivered already is the caller's question,
	// asked in the same step as reserving it. See reserve.
	return cfg.Path + "\x00" + presented, nil
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

// errDeliveryInFlight is a second copy of a delivery that is being
// delivered right now.
var errDeliveryInFlight = errors.New("this delivery is already being accepted")

// reserve claims a signature for one delivery, and refuses it when it
// has been delivered inside the window or is being delivered now. The
// check and the claim are one step under one lock, which is the point:
// as two steps, two copies arriving together both passed the check.
//
// The returned release gives the claim back, for a delivery that did
// not land; landed replaces it with the record of one that did. A key
// of "" -- a token hook, which has no nonce -- reserves nothing.
func (h *Hooks) reserve(key string, now time.Time, window time.Duration) (func(), error) {
	if key == "" {
		return func() {}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, at := range h.seen {
		if now.Sub(at) > window {
			delete(h.seen, k)
		}
	}
	if _, already := h.seen[key]; already {
		// Verified and already delivered: a replay inside the window,
		// which the signature alone cannot tell from the original.
		return nil, fmt.Errorf("this delivery has already been accepted")
	}
	if h.inFlight[key] {
		return nil, errDeliveryInFlight
	}
	h.inFlight[key] = true
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.inFlight, key)
	}, nil
}

// landed records a delivery that reached the bus, so the next one
// carrying the same signature is a replay, and ends its reservation.
func (h *Hooks) landed(key string, now time.Time) {
	if key == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen[key] = now
	delete(h.inFlight, key)
}

// allow is the per-path token bucket for deliveries that authenticated.
func (h *Hooks) allow(cfg *Hook, now time.Time) bool {
	return h.take(h.buckets, cfg, now)
}

// allowRefusal is the per-path bucket for deliveries that did not, at
// the same rate. It decides only whether a refusal is logged.
func (h *Hooks) allowRefusal(cfg *Hook, now time.Time) bool {
	return h.take(h.refusals, cfg, now)
}

func (h *Hooks) take(buckets map[string]*ratelimit.Bucket, cfg *Hook, now time.Time) bool {
	if cfg.RateLimit <= 0 {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := buckets[cfg.Path]
	if !ok {
		b = &ratelimit.Bucket{}
		buckets[cfg.Path] = b
	}
	return b.Take(now, cfg.RateLimit, cfg.RateBurst)
}
