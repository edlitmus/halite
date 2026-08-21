"""Extract (template, context, expected) triples from Jinja's own tests.

Only the mechanically unambiguous shape is taken: a template built from a
string literal by env.from_string, rendered with literal keyword arguments,
and compared with == against a string literal. Anything using a fixture, a
custom filter, an exception, or async is left alone.
"""
import ast, json, os, sys

LITERAL = (ast.Constant, ast.List, ast.Tuple, ast.Dict, ast.Set, ast.UnaryOp)

def literal(node):
    try:
        return ast.literal_eval(node)
    except Exception:
        return None, False
    # unreachable

def try_literal(node):
    try:
        return True, ast.literal_eval(node)
    except Exception:
        return False, None

def env_call(node):
    """env.from_string("...") -> the template string, else None."""
    if not isinstance(node, ast.Call):
        return None
    f = node.func
    if not isinstance(f, ast.Attribute) or f.attr != "from_string":
        return None
    if not isinstance(f.value, ast.Name) or f.value.id != "env":
        return None
    if not node.args:
        return None
    ok, v = try_literal(node.args[0])
    if not ok or not isinstance(v, str):
        return None
    if node.keywords:
        return None
    return v

def render_cmp(node):
    """assert <name>.render(**kw) == "..." -> (name, kwargs, expected)."""
    if not isinstance(node, ast.Assert):
        return None
    t = node.test
    if not isinstance(t, ast.Compare) or len(t.ops) != 1 or not isinstance(t.ops[0], ast.Eq):
        return None
    left, right = t.left, t.comparators[0]
    if not isinstance(left, ast.Call):
        return None
    f = left.func
    if not isinstance(f, ast.Attribute) or f.attr != "render":
        return None
    if not isinstance(f.value, ast.Name):
        return None
    ok, expected = try_literal(right)
    if not ok or not isinstance(expected, str):
        return None
    ctx = {}
    for kw in left.keywords:
        if kw.arg is None:
            return None
        ok, v = try_literal(kw.value)
        if not ok:
            return None
        ctx[kw.arg] = v
    for a in left.args:
        ok, v = try_literal(a)
        if not ok or not isinstance(v, dict):
            return None
        ctx.update(v)
    return f.value.id, ctx, expected

def walk_funcs(tree):
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            yield node

out = []
root = sys.argv[1]
for fn in sorted(os.listdir(root)):
    if not fn.startswith("test_") or not fn.endswith(".py"):
        continue
    if fn in ("test_async.py", "test_async_filters.py", "test_bytecode_cache.py",
              "test_pickle.py", "test_idtracking.py", "test_nodes.py",
              "test_debug.py", "test_utils.py", "test_loader.py", "test_compile.py"):
        continue
    src = open(os.path.join(root, fn)).read()
    tree = ast.parse(src)
    for func in walk_funcs(tree):
        if func.name.startswith("test_") is False:
            continue
        templates = {}
        for stmt in ast.walk(func):
            if isinstance(stmt, ast.Assign) and len(stmt.targets) == 1 and isinstance(stmt.targets[0], ast.Name):
                t = env_call(stmt.value)
                if t is not None:
                    templates[stmt.targets[0].id] = t
            r = render_cmp(stmt)
            if r is None:
                continue
            name, ctx, expected = r
            if name not in templates:
                continue
            out.append({
                "id": "%s::%s" % (fn[:-3], func.name),
                "template": templates[name],
                "context": ctx,
                "expected": expected,
            })

# De-duplicate, and number repeats within one test function.
seen = {}
for c in out:
    n = seen.get(c["id"], 0)
    seen[c["id"]] = n + 1
    if n:
        c["id"] = "%s#%d" % (c["id"], n)
print(json.dumps(out, indent=1, ensure_ascii=False, sort_keys=True))
sys.stderr.write("extracted %d cases\n" % len(out))
