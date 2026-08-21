"""Print PyYAML's reading of each document on stdin, in a tagged form.

Reads a JSON list of YAML documents and writes a JSON list of the same
length. Each entry is either {"err": "..."} or {"shape": "..."}, where the
shape is a canonical string carrying the resolved *type* as well as the
value — the type being the whole point of the comparison, since the two
implementations agree on the characters and can still disagree on whether
`0644` is a string or the integer 420.

The shape grammar is shared with differential_test.go and both sides must
spell it identically:

    null | bool:true | int:-3 | float:1.5 | str:text | bin:<base64>
    ts:<iso8601> | [a,b] | {k=v,k=v}
"""

import base64
import datetime
import json
import sys

import yaml


def shape(v):
    if v is None:
        return "null"
    if isinstance(v, bool):
        return "bool:true" if v else "bool:false"
    if isinstance(v, int):
        return "int:%d" % v
    if isinstance(v, float):
        if v != v:
            return "float:nan"
        if v == float("inf"):
            return "float:+inf"
        if v == float("-inf"):
            return "float:-inf"
        return "float:%.17g" % v
    if isinstance(v, str):
        return "str:" + v
    if isinstance(v, bytes):
        return "bin:" + base64.standard_b64encode(v).decode("ascii")
    if isinstance(v, (datetime.datetime, datetime.date)):
        return "ts:" + v.isoformat()
    if isinstance(v, list):
        return "[" + ",".join(shape(x) for x in v) + "]"
    if isinstance(v, (dict,)):
        return "{" + ",".join(sorted(shape(k) + "=" + shape(x) for k, x in v.items())) + "}"
    if isinstance(v, (set, frozenset)):
        return "set[" + ",".join(sorted(shape(x) for x in v)) + "]"
    return "unknown:" + type(v).__name__


def main():
    documents = json.load(sys.stdin)
    out = []
    for text in documents:
        try:
            # safe_load is what Salt uses for a state file, so it is the
            # behaviour worth matching rather than full_load's.
            out.append({"shape": shape(yaml.safe_load(text))})
        except Exception as e:  # noqa: BLE001 - any failure is "refused"
            out.append({"err": type(e).__name__ + ": " + str(e).replace("\n", " ")})
    json.dump(out, sys.stdout)


main()
