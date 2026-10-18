"""Generate internal/yaml/testdata/squote.jsonl.

PyYAML's single-quoted scalar, which is what Salt's `yaml_squote` filter
is (salt/utils/yamlencoding.py: an Emitter of unbounded width, and
write_single_quoted). Each line is {"in": text, "out": PyYAML's text}.
The inputs are random strings over the characters the emitter treats
specially -- the four line breaks, spaces, quotes -- and ordinary text.

Usage: python3 tools/make-yamlquote-corpus.py > internal/yaml/testdata/squote.jsonl
"""
import io
import json
import random
import sys

import yaml

rnd = random.Random(321)
ALPHABET = ["a", "b", "z", " ", "  ", "'", "''", '"', "\n", "\n\n", "\r", "\r\n",
            "\t", "\x85", " ", " ", "é", "日", "😀", "-", ":", "#", "\\"]


def squote(text):
    out = io.StringIO()
    yaml.emitter.Emitter(out, width=sys.maxsize).write_single_quoted(text)
    return out.getvalue()


fixed = ["", "a", "it's", "a\nb", "a\n\nb", "line1\nline2\n", "\n", "\nlead",
         " lead", "trail ", "-----BEGIN KEY-----\nAAA\nBBB\n-----END KEY-----\n"]
for t in fixed:
    print(json.dumps({"in": t, "out": squote(t)}, ensure_ascii=False))
for _ in range(2000):
    t = "".join(rnd.choice(ALPHABET) for _ in range(rnd.randint(1, 12)))
    print(json.dumps({"in": t, "out": squote(t)}, ensure_ascii=False))
