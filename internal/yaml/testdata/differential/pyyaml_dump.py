"""Dump a corpus of values with PyYAML, so the Go encoder can be held to
what Salt's YAML writer actually produces.

The corpus arrives as JSON on stdin: a list of values. Each is dumped in
block style, stripped, and returned in order. `safe_dump` appends a
document-end marker to a bare scalar, and Salt's own `yaml` filter strips
it, so this does too.
"""

import json
import sys

import yaml


def dump(value):
    text = yaml.safe_dump(value, default_flow_style=False).strip()
    if text.endswith("\n..."):
        text = text[: -len("\n...")]
    return text


def main():
    corpus = json.load(sys.stdin)
    json.dump([dump(v) for v in corpus], sys.stdout)


if __name__ == "__main__":
    main()
