package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// dataStoreFileName is the file a node's `data` store lives in, under
// cache_dir. One file rather than one-file-per-key, because a tree using
// this module wants a handful of small facts a state left for another
// state to read, not a filesystem of its own.
const dataStoreFileName = "datastore.json"

// registerData installs the `data` module of SPEC 15.2: a node-local
// key/value store a tree can read across separate state runs, separate
// execution module calls, and reboots.
//
// # Where it lives
//
// Everything here persists to one JSON file under cache_dir —
// cacheDirOf, the same lookup file_patch.go's backup cache already uses
// — because cache_dir is the one place SPEC already designates for a
// node to keep state about itself, and a second per-module convention
// would only be one more path an operator has to learn and back up. A
// node with no cache_dir configured has nowhere to keep this, and every
// function says so plainly rather than falling back to /tmp or the
// working directory, either of which would make the store disappear or
// move without anyone deciding that.
//
// # JSON, not msgpack or a pickle
//
// Real Salt's `data` module keeps a pickle (or msgpack, on newer
// installs); a Go reimplementation has no reason to add a serialization
// format when SPEC section 6.4 already settled on JSON as the form a
// halite component uses whenever structured data goes to disk, and
// value.EncodeJSON/DecodeJSON already exist and are what every other
// on-disk mapping in this tree uses. A store that only halite ever reads
// gets nothing from a second format, and loses the ability for an
// operator to `cat` it.
//
// # There is no in-memory session
//
// Real Salt's `dump` writes out a dict its long-running agent process has
// been accumulating in memory since it started; a halite exec module call is
// one process invocation with no such lifetime, so `set`, `update`, and
// `pop` write straight through to disk on every call rather than
// deferring to a `dump` that a caller might never make. `load` and
// `items` both return the whole store for the same reason: there is no
// separate "loaded into memory" state to distinguish them by, only "read
// from disk right now" — and `dump` here means what it means for `load`
// to have a counterpart: replace the whole store with a given mapping,
// not flush a memory this process does not keep.
//
// # Whole-file read-modify-write, not a database
//
// Nothing here asks for concurrent access from many callers at once — a
// state run is already serial on one node — so every mutating function
// reads the file, changes the in-memory mapping, and writes the whole
// thing back with writeAtomic. That gives torn-write safety and nothing
// more, because nothing more is a documented requirement, and a real
// database would be a dependency this project does not take for a
// handful of key/value pairs.
//
// # Change detection compares encoded values, not Go types
//
// A value written by `set` arrives as whatever Go type the caller's
// argument decoded to; the same value read back after a round trip
// through JSON comes back as value.DecodeJSON's own types, which are not
// always the same Go type (an int64 becomes a json.Number, for
// instance). Comparing the two with `==` or reflect.DeepEqual would
// report a change every time a value is set to what it already holds.
// jsonEqual instead compares each side's own canonical JSON encoding,
// which is stable across that round trip and needs no per-type case.
func registerData(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "load",
				Doc:      "Return everything this node's data store holds.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataLoadFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "items",
				Doc:      "Return everything this node's data store holds. An alias for load.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataLoadFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "dump",
				Doc: "Replace the whole store with a given mapping.",
				Params: []signature.Param{
					req("items", signature.Map, "The mapping to store, replacing whatever was there."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: dataDumpFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "get",
				Doc: "Return one key's value, or a default when it is absent.",
				Params: []signature.Param{
					req("key", signature.String, "The key to read."),
					opt("default", signature.Any, nil, "Returned when the key is not set."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataGetFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "set",
				Doc: "Set one key's value.",
				Params: []signature.Param{
					req("key", signature.String, "The key to write."),
					req("value", signature.Any, "The value to store."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: dataSetFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "has",
				Doc: "Report whether a key is set.",
				Params: []signature.Param{
					req("key", signature.String, "The key to look for."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataHasFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "keys",
				Doc:      "Return every key the store holds.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataKeysFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "values",
				Doc:      "Return every value the store holds, in key order.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: dataValuesFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "update",
				Doc: "Merge a mapping into the store, adding or overwriting the keys it names and leaving the rest untouched.",
				Params: []signature.Param{
					req("items", signature.Map, "The keys and values to merge in."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: dataUpdateFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "pop",
				Doc: "Remove a key and return the value it held, or a default when it was absent.",
				Params: []signature.Param{
					req("key", signature.String, "The key to remove."),
					opt("default", signature.Any, nil, "Returned when the key was not set."),
				},
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: dataPopFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "data", Function: "clear",
				Doc:      "Empty the store.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: dataClearFn,
		},
	)
}

// ---- persistence ----

func dataStorePath(c *exec.Context) (string, error) {
	root := cacheDirOf(c)
	if root == "" {
		return "", errors.New("this node has no cache_dir, so `data` has nowhere to keep its store")
	}
	return filepath.Join(root, dataStoreFileName), nil
}

// dataReadStore reads the store, treating an absent or empty file as an
// empty mapping rather than an error: a node that has never called `set`
// has an empty store, not a broken one.
func dataReadStore(path string) (*value.Map, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return value.NewMap(8), nil
		}
		return nil, fmt.Errorf("%s could not be read: %w", path, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return value.NewMap(8), nil
	}
	decoded, err := value.DecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s does not hold valid JSON: %w", path, err)
	}
	m, ok := decoded.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s holds a %s, not a mapping", path, value.TypeName(decoded))
	}
	return m, nil
}

func dataWriteStore(path string, m *value.Map) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s could not be created: %w", filepath.Dir(path), err)
	}
	encoded, err := value.EncodeJSON(m, 2)
	if err != nil {
		return err
	}
	// 0600: a tree can put anything in this store, including something it
	// should not have written in plain sight, and this module has no way
	// to know which. The private-key convention x509 and tls follow is
	// the right default here too.
	return writeAtomic(path, encoded, 0o600)
}

// jsonEqual compares two decoded values by their own canonical JSON
// encoding, which is stable across the round trip a value takes through
// dataReadStore even when the two sides arrived as different Go types
// for the same JSON value.
func jsonEqual(a, b any) bool {
	ea, errA := value.EncodeJSON(a, 0)
	eb, errB := value.EncodeJSON(b, 0)
	if errA != nil || errB != nil {
		return false
	}
	return string(ea) == string(eb)
}

// mapsEqual reports whether two mappings hold the same keys with
// jsonEqual values, independent of entry order.
func mapsEqual(a, b *value.Map) bool {
	if a.Len() != b.Len() {
		return false
	}
	for _, e := range a.Entries() {
		key := value.KeyString(e.Key)
		bv, ok := b.GetString(key)
		if !ok || !jsonEqual(e.Val, bv) {
			return false
		}
	}
	return true
}

// ---- reading ----

func dataLoadFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	return dataReadStore(path)
}

func dataGetFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if key == "" {
		return nil, errors.New("a key must be named")
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	if v, ok := store.GetString(key); ok {
		return v, nil
	}
	def, _ := args.Get("default")
	return def, nil
}

func dataHasFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if key == "" {
		return nil, errors.New("a key must be named")
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	return store.Has(key), nil
}

func dataKeysFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	keys := store.StringKeys()
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = k
	}
	return out, nil
}

func dataValuesFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	entries := store.Entries()
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e.Val
	}
	return out, nil
}

// ---- writing ----

func dataMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

func dataSetFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if key == "" {
		return nil, errors.New("a key must be named")
	}
	val, _ := args.Get("value")
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	old, existed := store.GetString(key)
	if existed && jsonEqual(old, val) {
		return dataMutateResult(c, false, fmt.Sprintf("%s already holds that value.", key), nil), nil
	}
	var oldForChange any
	if existed {
		oldForChange = old
	}
	change := value.MapOf(key, states.Change(oldForChange, val))
	if c.Test {
		return dataMutateResult(c, true, fmt.Sprintf("%s would be set.", key), change), nil
	}
	store.Set(key, val)
	if err := dataWriteStore(path, store); err != nil {
		return nil, err
	}
	return dataMutateResult(c, true, fmt.Sprintf("%s was set.", key), change), nil
}

func dataUpdateFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	items := states.Mapping(args, "items")
	if items == nil || items.Len() == 0 {
		return dataMutateResult(c, false, "no keys were given to update.", nil), nil
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	change := value.NewMap(items.Len())
	for _, e := range items.Entries() {
		key := value.KeyString(e.Key)
		old, existed := store.GetString(key)
		if existed && jsonEqual(old, e.Val) {
			continue
		}
		var oldForChange any
		if existed {
			oldForChange = old
		}
		change.Set(key, states.Change(oldForChange, e.Val))
	}
	if change.Len() == 0 {
		return dataMutateResult(c, false, "every key already held the value given.", nil), nil
	}
	if c.Test {
		return dataMutateResult(c, true, fmt.Sprintf("%d key(s) would be updated.", change.Len()), change), nil
	}
	for _, e := range items.Entries() {
		store.Set(value.KeyString(e.Key), e.Val)
	}
	if err := dataWriteStore(path, store); err != nil {
		return nil, err
	}
	return dataMutateResult(c, true, fmt.Sprintf("%d key(s) were updated.", change.Len()), change), nil
}

func dataDumpFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	items := states.Mapping(args, "items")
	if items == nil {
		items = value.NewMap(0)
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	if mapsEqual(store, items) {
		return dataMutateResult(c, false, "the store already holds that mapping.", nil), nil
	}
	change := value.MapOf("store", states.Change(fmt.Sprintf("%d key(s)", store.Len()), fmt.Sprintf("%d key(s)", items.Len())))
	if c.Test {
		return dataMutateResult(c, true, "the store would be replaced.", change), nil
	}
	if err := dataWriteStore(path, items); err != nil {
		return nil, err
	}
	return dataMutateResult(c, true, "the store was replaced.", change), nil
}

// dataPopResult carries the value pop hands back to the caller alongside
// the changed/comment/changes shape every mutating function here uses --
// a plain return of just the value would give the caller no honest way
// to tell, under test mode, whether the key was actually there to
// remove.
func dataPopResult(c *exec.Context, popped any, changed bool, comment string, change *value.Map) *value.Map {
	out := dataMutateResult(c, changed, comment, change)
	out.Set("value", popped)
	return out
}

func dataPopFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(states.Str(args, "key", ""))
	if key == "" {
		return nil, errors.New("a key must be named")
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	old, existed := store.GetString(key)
	if !existed {
		def, _ := args.Get("default")
		return dataPopResult(c, def, false, fmt.Sprintf("%s was not set.", key), nil), nil
	}
	change := value.MapOf(key, states.Change(old, nil))
	if c.Test {
		return dataPopResult(c, old, true, fmt.Sprintf("%s would be removed.", key), change), nil
	}
	store.Delete(key)
	if err := dataWriteStore(path, store); err != nil {
		return nil, err
	}
	return dataPopResult(c, old, true, fmt.Sprintf("%s was removed.", key), change), nil
}

func dataClearFn(c *exec.Context, args *value.Map) (any, error) {
	path, err := dataStorePath(c)
	if err != nil {
		return nil, err
	}
	store, err := dataReadStore(path)
	if err != nil {
		return nil, err
	}
	if store.Len() == 0 {
		return dataMutateResult(c, false, "the store is already empty.", nil), nil
	}
	change := value.MapOf("store", states.Change(fmt.Sprintf("%d key(s)", store.Len()), "0 key(s)"))
	if c.Test {
		return dataMutateResult(c, true, "the store would be cleared.", change), nil
	}
	if err := dataWriteStore(path, value.NewMap(0)); err != nil {
		return nil, err
	}
	return dataMutateResult(c, true, "the store was cleared.", change), nil
}
