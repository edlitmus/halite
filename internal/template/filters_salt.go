package template

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// addSaltFilters installs the filters Salt adds on top of Jinja's, from
// SPEC section 10.2.4. Everything here is written against the standard
// library; nothing calls out to a module.
func addSaltFilters(f map[string]FilterFunc) {
	addSerializationFilters(f)
	addHashFilters(f)
	addRegexFilters(f)
	addCollectionFilters(f)
	addNetworkFilters(f)
	addMiscFilters(f)
}

func addSerializationFilters(f map[string]FilterFunc) {
	f["yaml_encode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		return strings.TrimRight(yaml.Encode(v, yaml.EncodeOptions{Flow: true}), "\n"), nil
	}
	f["yaml_dquote"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return yaml.Quote(s), nil
	}
	f["yaml_squote"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return yaml.SingleQuote(s), nil
	}
	f["json_encode_dict"] = jsonFilter
	f["json_encode_list"] = jsonFilter
	f["json_decode_dict"] = jsonDecode(true)
	f["json_decode_list"] = jsonDecode(false)
	f["load_json"] = jsonDecode(true)
	f["load_yaml"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		out, _, err := yaml.Parse([]byte(s), yaml.DefaultOptions("<load_yaml>"))
		if err != nil {
			return nil, fc.Errorf("load_yaml: %v", err)
		}
		return out, nil
	}

	f["to_bool"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "true", "yes", "y", "on", "1":
				return true, nil
			case "false", "no", "n", "off", "0", "":
				return false, nil
			}
			return nil, fc.Errorf("to_bool: %q is not a boolean", t)
		case nil:
			return false, nil
		}
		if n, ok := asFloat(v); ok {
			return n != 0, nil
		}
		return truthy(v), nil
	}

	f["to_num"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		if n, ok := asInt(v); ok {
			return n, nil
		}
		if n, ok := asFloat(v); ok {
			return n, nil
		}
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, nil
		}
		return nil, fc.Errorf("to_num: %q is not a number", s)
	}

	f["quote"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'", nil
	}

	f["dict_to_sls_yaml_params"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		m, ok := v.(*value.Map)
		if !ok {
			return nil, fc.Errorf("dict_to_sls_yaml_params expects a mapping")
		}
		var b strings.Builder
		for _, e := range m.Entries() {
			b.WriteString("- ")
			b.WriteString(value.KeyString(e.Key))
			b.WriteString(": ")
			b.WriteString(strings.TrimRight(yaml.Encode(e.Val, yaml.EncodeOptions{Flow: true}), "\n"))
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
}

func jsonDecode(wantMap bool) FilterFunc {
	return func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		out, err := decodeJSONOrdered([]byte(s))
		if err != nil {
			return nil, fc.Errorf("decoding JSON: %v", err)
		}
		if wantMap {
			if _, ok := out.(*value.Map); !ok {
				return nil, fc.Errorf("expected a JSON object, found %s", typeName(out))
			}
		} else if _, ok := out.([]any); !ok {
			return nil, fc.Errorf("expected a JSON array, found %s", typeName(out))
		}
		return out, nil
	}
}

// decodeJSONOrdered decodes JSON into the ordered model, keeping object
// key order rather than losing it to a Go map.
func decodeJSONOrdered(b []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := value.NewMap(4)
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				m.Set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			out := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, nil
		}
		fl, err := t.Float64()
		if err != nil {
			return nil, err
		}
		return fl, nil
	}
	return tok, nil
}

func addHashFilters(f map[string]FilterFunc) {
	hashOf := func(name string, sum func([]byte) string) FilterFunc {
		return func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
			s, err := fc.Str(v)
			if err != nil {
				return nil, err
			}
			return sum([]byte(s)), nil
		}
	}
	f["md5"] = hashOf("md5", func(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) })
	f["sha1"] = hashOf("sha1", func(b []byte) string { h := sha1.Sum(b); return hex.EncodeToString(h[:]) })
	f["sha256"] = hashOf("sha256", func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) })
	f["sha512"] = hashOf("sha512", func(b []byte) string { h := sha512.Sum512(b); return hex.EncodeToString(h[:]) })

	f["hmac"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		msg, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		key, ok := argString(args, kwargs, 0, "shared_secret")
		if !ok {
			return nil, fc.Errorf("hmac needs a shared secret")
		}
		challenge, hasChallenge := argString(args, kwargs, 1, "challenge_hmac")
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(msg))
		sum := mac.Sum(nil)
		if hasChallenge {
			want, err := base64.StdEncoding.DecodeString(challenge)
			if err != nil {
				return false, nil
			}
			return hmac.Equal(sum, want), nil
		}
		return base64.StdEncoding.EncodeToString(sum), nil
	}

	f["base64_encode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString([]byte(s)), nil
	}
	f["base64_decode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return nil, fc.Errorf("base64_decode: %v", err)
		}
		return string(b), nil
	}
	f["hex_encode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return hex.EncodeToString([]byte(s)), nil
	}
	f["hex_decode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		b, err := hex.DecodeString(strings.TrimSpace(s))
		if err != nil {
			return nil, fc.Errorf("hex_decode: %v", err)
		}
		return string(b), nil
	}

	// random_hash and rand_str draw on the render's deterministic source
	// so that a test run and the real run agree; uuid draws on
	// crypto/rand, because a UUID that repeats across runs is worse than
	// one that differs.
	f["random_hash"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		size := int64(32)
		if n, ok := arg(args, kwargs, 0, "size"); ok {
			size, _ = asInt(n)
		}
		if size <= 0 || size > 1024 {
			return nil, fc.Errorf("random_hash size must be between 1 and 1024")
		}
		b := make([]byte, size)
		for i := range b {
			b[i] = byte(fc.Rand().Intn(256))
		}
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:]), nil
	}

	f["rand_str"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		return f["random_hash"](fc, v, args, kwargs)
	}

	f["uuid"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		if s != "" {
			// A named UUID is version 5 over the DNS namespace, matching
			// Salt, so the same name always yields the same UUID.
			return uuid5(s), nil
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, fc.Errorf("uuid: %v", err)
		}
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		return formatUUID(b), nil
	}
}

// dnsNamespace is RFC 4122's DNS namespace UUID, which is what Salt's
// uuid filter uses.
var dnsNamespace = [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

func uuid5(name string) string {
	h := sha1.New()
	h.Write(dnsNamespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func addRegexFilters(f map[string]FilterFunc) {
	f["regex_escape"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return regexpQuoteMeta(s), nil
	}

	compileArg := func(fc *FilterContext, args []any, kwargs map[string]any, i int, name string) (string, error) {
		p, ok := argString(args, kwargs, i, name)
		if !ok {
			return "", fc.Errorf("this filter needs a pattern")
		}
		return p, nil
	}

	f["regex_match"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		pattern, err := compileArg(fc, args, kwargs, 0, "pattern")
		if err != nil {
			return nil, err
		}
		re, err := regexcompat.CompileWithFlags("^(?:"+pattern+")", truthy(kwargs["ignorecase"]), truthy(kwargs["multiline"]), false)
		if err != nil {
			return nil, fc.Errorf("%v", err)
		}
		m := re.FindStringSubmatch(s)
		if m == nil {
			return nil, nil
		}
		if len(m) == 1 {
			return []any{}, nil
		}
		return stringsToAny(m[1:]), nil
	}

	f["regex_search"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		pattern, err := compileArg(fc, args, kwargs, 0, "pattern")
		if err != nil {
			return nil, err
		}
		re, err := regexcompat.CompileWithFlags(pattern, truthy(kwargs["ignorecase"]), truthy(kwargs["multiline"]), false)
		if err != nil {
			return nil, fc.Errorf("%v", err)
		}
		m := re.FindStringSubmatch(s)
		if m == nil {
			return nil, nil
		}
		if len(m) == 1 {
			return []any{}, nil
		}
		return stringsToAny(m[1:]), nil
	}

	f["regex_replace"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		pattern, err := compileArg(fc, args, kwargs, 0, "pattern")
		if err != nil {
			return nil, err
		}
		repl, ok := argString(args, kwargs, 1, "repl")
		if !ok {
			return nil, fc.Errorf("regex_replace needs a replacement")
		}
		re, err := regexcompat.CompileWithFlags(pattern, truthy(kwargs["ignorecase"]), truthy(kwargs["multiline"]), false)
		if err != nil {
			return nil, fc.Errorf("%v", err)
		}
		return re.ReplaceAllString(s, pythonReplacement(repl)), nil
	}

	f["regex_split"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		pattern, err := compileArg(fc, args, kwargs, 0, "pattern")
		if err != nil {
			return nil, err
		}
		re, err := regexcompat.Compile(pattern)
		if err != nil {
			return nil, fc.Errorf("%v", err)
		}
		return stringsToAny(re.Split(s, -1)), nil
	}
}

// pythonReplacement rewrites Python's \1 group references into Go's ${1},
// so a pattern carried over from a Salt tree substitutes the same way.
func pythonReplacement(repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		if repl[i] == '\\' && i+1 < len(repl) && repl[i+1] >= '0' && repl[i+1] <= '9' {
			j := i + 1
			for j < len(repl) && repl[j] >= '0' && repl[j] <= '9' {
				j++
			}
			b.WriteString("${" + repl[i+1:j] + "}")
			i = j - 1
			continue
		}
		if repl[i] == '$' {
			b.WriteString("$$")
			continue
		}
		b.WriteByte(repl[i])
	}
	return b.String()
}

func regexpQuoteMeta(s string) string {
	const special = `\.+*?()|[]{}^$`
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(special, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func addCollectionFilters(f map[string]FilterFunc) {
	f["union"] = setOp("union", true, true, true)
	f["intersect"] = setOp("intersect", false, true, false)
	f["difference"] = setOp("difference", true, false, false)
	f["symmetric_difference"] = setOp("symmetric_difference", true, false, true)

	f["flatten"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		levels := -1
		if n, ok := arg(args, kwargs, 0, "levels"); ok {
			l, _ := asInt(n)
			levels = int(l)
		}
		var walk func([]any, int) []any
		walk = func(in []any, depth int) []any {
			out := []any{}
			for _, item := range in {
				sub, ok := item.([]any)
				if ok && (levels < 0 || depth < levels) {
					out = append(out, walk(sub, depth+1)...)
					continue
				}
				out = append(out, item)
			}
			return out
		}
		return walk(items, 0), nil
	}

	f["zip"] = zipFilter(false)
	f["zip_longest"] = zipFilter(true)

	f["avg"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		ns, err := numbersOf(fc, v)
		if err != nil {
			return nil, err
		}
		return mean(ns), nil
	}
	f["stdev"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		ns, err := numbersOf(fc, v)
		if err != nil {
			return nil, err
		}
		return stdev(ns), nil
	}

	f["combinations"] = combinatoric(false)
	f["permutations"] = combinatoric(true)

	f["is_list"] = func(_ *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		_, ok := v.([]any)
		return ok, nil
	}
	f["is_iter"] = func(_ *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		switch v.(type) {
		case []any, *value.Map, string:
			return true, nil
		}
		return false, nil
	}

	f["traverse"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		p, ok := argString(args, kwargs, 0, "key")
		if !ok {
			return nil, fc.Errorf("traverse needs a key path")
		}
		delim := ":"
		if d, ok := argString(args, kwargs, 2, "delimiter"); ok {
			delim = d
		}
		if out, found := value.Traverse(v, p, delim); found {
			return out, nil
		}
		if def, ok := arg(args, kwargs, 1, "default"); ok {
			return def, nil
		}
		return Undefined{Name: p, Pos: fc.Pos, Hint: "the path did not resolve"}, nil
	}

	dictKeyOp := func(name string, apply func(cur, v any) (any, error)) FilterFunc {
		return func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
			p, ok := argString(args, kwargs, 0, "key")
			if !ok {
				return nil, fc.Errorf("%s needs a key path", name)
			}
			newVal, ok := arg(args, kwargs, 1, "value")
			if !ok {
				return nil, fc.Errorf("%s needs a value", name)
			}
			delim := ":"
			if d, ok := argString(args, kwargs, 2, "delimiter"); ok {
				delim = d
			}
			root, ok := value.Deep(v).(*value.Map)
			if !ok {
				return nil, fc.Errorf("%s expects a mapping, found %s", name, typeName(v))
			}
			parts := strings.Split(p, delim)
			cur := root
			for _, part := range parts[:len(parts)-1] {
				next, found := cur.Get(part)
				nm, isMap := next.(*value.Map)
				if !found || !isMap {
					nm = value.NewMap(2)
					cur.Set(part, nm)
				}
				cur = nm
			}
			leaf := parts[len(parts)-1]
			existing, _ := cur.Get(leaf)
			merged, err := apply(existing, newVal)
			if err != nil {
				return nil, fc.Errorf("%s: %v", name, err)
			}
			cur.Set(leaf, merged)
			return root, nil
		}
	}

	f["set_dict_key_value"] = dictKeyOp("set_dict_key_value", func(_, v any) (any, error) { return v, nil })
	f["update_dict_key_value"] = dictKeyOp("update_dict_key_value", func(cur, v any) (any, error) {
		curMap, ok1 := cur.(*value.Map)
		newMap, ok2 := v.(*value.Map)
		if !ok2 {
			return nil, fmt.Errorf("the value must be a mapping")
		}
		if !ok1 {
			return newMap, nil
		}
		return value.Merge(curMap, newMap, value.MergeOpts{Strategy: value.Recurse}), nil
	})
	f["append_dict_key_value"] = dictKeyOp("append_dict_key_value", func(cur, v any) (any, error) {
		list, _ := cur.([]any)
		return append(append([]any{}, list...), v), nil
	})
	f["extend_dict_key_value"] = dictKeyOp("extend_dict_key_value", func(cur, v any) (any, error) {
		add, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("the value must be a sequence")
		}
		list, _ := cur.([]any)
		return append(append([]any{}, list...), add...), nil
	})
}

func zipFilter(longest bool) FilterFunc {
	return func(fc *FilterContext, v any, args []any, _ map[string]any) (any, error) {
		seqs := [][]any{}
		first, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		seqs = append(seqs, first)
		for _, a := range args {
			s, err := asSeq(fc, a)
			if err != nil {
				return nil, err
			}
			seqs = append(seqs, s)
		}
		n := len(seqs[0])
		for _, s := range seqs[1:] {
			if longest && len(s) > n {
				n = len(s)
			}
			if !longest && len(s) < n {
				n = len(s)
			}
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			row := make([]any, len(seqs))
			for j, s := range seqs {
				if i < len(s) {
					row[j] = s[i]
				}
			}
			out = append(out, row)
		}
		return out, nil
	}
}

func combinatoric(ordered bool) FilterFunc {
	return func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		r := int64(len(items))
		if n, ok := arg(args, kwargs, 0, "r"); ok {
			r, _ = asInt(n)
		}
		if r < 0 || r > int64(len(items)) {
			return []any{}, nil
		}
		out := []any{}
		used := make([]bool, len(items))
		cur := make([]any, 0, r)
		var walk func(start int) error
		walk = func(start int) error {
			if int64(len(cur)) == r {
				out = append(out, append([]any{}, cur...))
				if int64(len(out)) > fc.r.opts.MaxIterations {
					return fc.Errorf("this combination would produce more than %d results", fc.r.opts.MaxIterations)
				}
				return nil
			}
			for i := 0; i < len(items); i++ {
				if !ordered && i < start {
					continue
				}
				if ordered && used[i] {
					continue
				}
				used[i] = true
				cur = append(cur, items[i])
				if err := walk(i + 1); err != nil {
					return err
				}
				cur = cur[:len(cur)-1]
				used[i] = false
			}
			return nil
		}
		if err := walk(0); err != nil {
			return nil, err
		}
		return out, nil
	}
}

func addNetworkFilters(f map[string]FilterFunc) {
	// The address filters accept a single value or a sequence and return
	// the members that match, which is what Salt's ipaddr family does.
	addrFilter := func(name string, keep func(netip.Addr) bool) FilterFunc {
		return func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
			if s, ok := v.(string); ok {
				a, err := parseAddrOrPrefix(s)
				if err != nil || !keep(a) {
					return nil, nil
				}
				return s, nil
			}
			items, err := asSeq(fc, v)
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, item := range items {
				s, ok := item.(string)
				if !ok {
					continue
				}
				a, err := parseAddrOrPrefix(s)
				if err != nil || !keep(a) {
					continue
				}
				out = append(out, s)
			}
			return out, nil
		}
	}
	f["ipaddr"] = addrFilter("ipaddr", func(a netip.Addr) bool { return a.IsValid() })
	f["ipv4"] = addrFilter("ipv4", func(a netip.Addr) bool { return a.Is4() })
	f["ipv6"] = addrFilter("ipv6", func(a netip.Addr) bool { return a.Is6() && !a.Is4In6() })

	isFilter := func(want func(netip.Addr) bool) FilterFunc {
		return func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
			s, err := fc.Str(v)
			if err != nil {
				return nil, err
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				return false, nil
			}
			return want(a), nil
		}
	}
	f["is_ip"] = isFilter(func(a netip.Addr) bool { return a.IsValid() })
	f["is_ipv4"] = isFilter(func(a netip.Addr) bool { return a.Is4() })
	f["is_ipv6"] = isFilter(func(a netip.Addr) bool { return a.Is6() && !a.Is4In6() })

	f["ip_host"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			return p.Addr().String() + "/" + strconv.Itoa(p.Bits()), nil
		}
		return s, nil
	}

	f["network_hosts"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fc.Errorf("network_hosts: %q is not a network", s)
		}
		size := prefixSize(p)
		if size > 65536 {
			return nil, fc.Errorf("network_hosts: %s holds %d addresses, which is more than this filter will enumerate", s, size)
		}
		out := []any{}
		addr := p.Masked().Addr()
		for i := int64(0); i < size; i++ {
			if i > 0 && i < size-1 || p.Bits() >= addr.BitLen()-1 {
				out = append(out, addr.String())
			}
			addr = addr.Next()
		}
		return out, nil
	}

	f["network_size"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fc.Errorf("network_size: %q is not a network", s)
		}
		return prefixSize(p), nil
	}

	f["cidr_merge"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		items, err := asSeq(fc, v)
		if err != nil {
			return nil, err
		}
		var prefixes []netip.Prefix
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if p, err := netip.ParsePrefix(s); err == nil {
				prefixes = append(prefixes, p.Masked())
				continue
			}
			if a, err := netip.ParseAddr(s); err == nil {
				prefixes = append(prefixes, netip.PrefixFrom(a, a.BitLen()))
			}
		}
		merged := mergePrefixes(prefixes)
		out := make([]any, len(merged))
		for i, p := range merged {
			out[i] = p.String()
		}
		return out, nil
	}

	f["cidr_subnets"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fc.Errorf("cidr_subnets: %q is not a network", s)
		}
		n, ok := arg(args, kwargs, 0, "prefixlen")
		bits, _ := asInt(n)
		if !ok || int(bits) <= p.Bits() || int(bits) > p.Addr().BitLen() {
			return nil, fc.Errorf("cidr_subnets needs a prefix length longer than %d", p.Bits())
		}
		count := int64(1) << (bits - int64(p.Bits()))
		if count > 65536 {
			return nil, fc.Errorf("cidr_subnets: %d subnets is more than this filter will enumerate", count)
		}
		out := make([]any, 0, count)
		addr := p.Masked().Addr()
		step := int64(1) << (int64(p.Addr().BitLen()) - bits)
		for i := int64(0); i < count; i++ {
			out = append(out, netip.PrefixFrom(addr, int(bits)).String())
			for j := int64(0); j < step; j++ {
				addr = addr.Next()
			}
		}
		return out, nil
	}

	f["gen_mac"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		prefix := "AC:DE:48"
		if s, ok := argString(args, kwargs, 0, "prefix"); ok {
			prefix = s
		}
		return fmt.Sprintf("%s:%02X:%02X:%02X", prefix,
			fc.Rand().Intn(256), fc.Rand().Intn(256), fc.Rand().Intn(256)), nil
	}

	f["mac_str_to_bytes"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		hw, err := net.ParseMAC(s)
		if err != nil {
			return nil, fc.Errorf("mac_str_to_bytes: %v", err)
		}
		return string(hw), nil
	}

	// dns_check resolves a name, so it is a network call from a template.
	// It stays available because trees use it, and it is bounded by a
	// timeout rather than left to the resolver's default.
	f["dns_check"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		host, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupHost(dnsContext(), host)
		if err != nil || len(addrs) == 0 {
			return nil, fc.Errorf("dns_check: %s did not resolve", host)
		}
		return addrs[0], nil
	}
}

func parseAddrOrPrefix(s string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Addr{}, err
	}
	return p.Addr(), nil
}

func prefixSize(p netip.Prefix) int64 {
	host := p.Addr().BitLen() - p.Bits()
	if host >= 63 {
		return int64(1) << 62
	}
	return int64(1) << host
}

// mergePrefixes collapses adjacent and contained networks.
func mergePrefixes(in []netip.Prefix) []netip.Prefix {
	if len(in) == 0 {
		return nil
	}
	sortPrefixes(in)
	out := []netip.Prefix{in[0]}
	for _, p := range in[1:] {
		last := out[len(out)-1]
		if last.Overlaps(p) {
			if last.Bits() <= p.Bits() {
				continue
			}
			out[len(out)-1] = p
			continue
		}
		out = append(out, p)
	}
	return out
}

func sortPrefixes(ps []netip.Prefix) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0; j-- {
			a, b := ps[j-1], ps[j]
			if a.Addr().Less(b.Addr()) || (a.Addr() == b.Addr() && a.Bits() <= b.Bits()) {
				break
			}
			ps[j-1], ps[j] = ps[j], ps[j-1]
		}
	}
}

func addMiscFilters(f map[string]FilterFunc) {
	f["strftime"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		layout := "%Y-%m-%d"
		if s, ok := argString(args, kwargs, 0, "format"); ok {
			layout = s
		}
		t, err := asTime(v)
		if err != nil {
			return nil, fc.Errorf("strftime: %v", err)
		}
		return strftime(t, layout), nil
	}
	f["date_format"] = f["strftime"]

	f["human_to_bytes"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		n, err := humanToBytes(s)
		if err != nil {
			return nil, fc.Errorf("human_to_bytes: %v", err)
		}
		return n, nil
	}

	f["sizeof_fmt"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		n, ok := asFloat(v)
		if !ok {
			return nil, fc.Errorf("sizeof_fmt expects a number")
		}
		binary := true
		if b, ok := arg(args, kwargs, 0, "binary"); ok {
			binary = truthy(b)
		}
		return humanSize(n, binary), nil
	}

	f["path_join"] = func(fc *FilterContext, v any, args []any, _ map[string]any) (any, error) {
		parts := []string{}
		add := func(x any) error {
			switch t := x.(type) {
			case string:
				parts = append(parts, t)
			case []any:
				for _, item := range t {
					s, ok := item.(string)
					if !ok {
						return fc.Errorf("path_join takes strings")
					}
					parts = append(parts, s)
				}
			default:
				return fc.Errorf("path_join takes strings, found %s", typeName(x))
			}
			return nil
		}
		if err := add(v); err != nil {
			return nil, err
		}
		for _, a := range args {
			if err := add(a); err != nil {
				return nil, err
			}
		}
		return path.Join(parts...), nil
	}

	f["method_call"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		name, ok := argString(args, kwargs, 0, "method")
		if !ok {
			return nil, fc.Errorf("method_call needs a method name")
		}
		m, err := fc.r.getAttr(v, name, fc.Pos)
		if err != nil {
			return nil, err
		}
		rest := []any{}
		if len(args) > 1 {
			rest = args[1:]
		}
		return fc.r.callValue(m, rest, nil, fc.Pos)
	}

	// `which` reports the path of a program. It is a filesystem read from
	// a template, which is why it is here rather than being pushed into a
	// module: existing trees use it in exactly this position.
	f["which"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		name, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		p, err := lookPath(name)
		if err != nil {
			return nil, nil
		}
		return p, nil
	}
}

func humanToBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q has no leading number", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToUpper(strings.TrimSpace(s[i:]))
	unit = strings.TrimSuffix(unit, "B")
	mult := map[string]float64{
		"": 1, "K": 1 << 10, "KI": 1 << 10, "M": 1 << 20, "MI": 1 << 20,
		"G": 1 << 30, "GI": 1 << 30, "T": 1 << 40, "TI": 1 << 40,
		"P": 1 << 50, "PI": 1 << 50,
	}
	m, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	return int64(n * m), nil
}

func asTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case int64:
		return time.Unix(t, 0).UTC(), nil
	case float64:
		return time.Unix(int64(t), 0).UTC(), nil
	case string:
		if t == "" || t == "now" {
			return time.Now(), nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, nil
			}
		}
		return time.Time{}, fmt.Errorf("%q is not a timestamp", t)
	case nil:
		return time.Now(), nil
	}
	return time.Time{}, fmt.Errorf("%s is not a timestamp", typeName(v))
}

// strftime translates the C format directives that SLS trees use.
func strftime(t time.Time, format string) string {
	replacements := []struct{ verb, layout string }{
		{"%Y", "2006"}, {"%y", "06"}, {"%m", "01"}, {"%d", "02"},
		{"%H", "15"}, {"%M", "04"}, {"%S", "05"},
		{"%b", "Jan"}, {"%B", "January"}, {"%a", "Mon"}, {"%A", "Monday"},
		{"%p", "PM"}, {"%I", "03"}, {"%Z", "MST"}, {"%z", "-0700"},
		{"%j", "002"}, {"%f", "000000"},
	}
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		verb := format[i : i+2]
		if verb == "%%" {
			b.WriteByte('%')
			i++
			continue
		}
		if verb == "%s" {
			b.WriteString(strconv.FormatInt(t.Unix(), 10))
			i++
			continue
		}
		matched := false
		for _, r := range replacements {
			if r.verb == verb {
				b.WriteString(t.Format(r.layout))
				matched = true
				break
			}
		}
		if !matched {
			b.WriteString(verb)
		}
		i++
	}
	return b.String()
}
