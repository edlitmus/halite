"""Generate internal/template/testdata/pyformat/{format,str}.tsv.

Python's own answers for its format-specification mini-language and for
str() of a float, which halite's str.format and float rendering are held
to (internal/template/pyformat.go). The cases are random, from a fixed
seed, over values chosen for the edges: ties, signed zeros, infinities,
NaN, values a float cannot hold exactly, and specs with every field.

Each line of format.tsv is: kind, value, spec, and Python's result, or
"!" and the exception's name when Python refuses. kind is s, i, b or f;
a float is written with repr(), so it reads back exactly. Each line of
str.tsv is repr(x) and str(x).

Usage: python3 tools/make-pyformat-corpus.py internal/template/testdata/pyformat
"""
import math
import os
import random
import struct
import sys

out = sys.argv[1]
os.makedirs(out, exist_ok=True)
rnd = random.Random(52)

INTS = [0, 1, -1, 7, 42, -42, 255, 1000, -1000, 1234567, -1234567, 2**31,
        10**12, -10**15]
FLOATS = [0.0, -0.0, 1.0, -1.0, 0.5, 1.5, 2.5, 3.14159, -3.14159, 100.0,
          1e16, 1.5e-7, 123456.789, -0.000123, 1e300, 2.675, math.inf,
          -math.inf, math.nan, 0.1, 99.995, 1234.5]
STRS = ["", "a", "abc", "hello world", "é", "日本"]


def spec(kind):
    s = ""
    if rnd.random() < 0.4:
        s += rnd.choice(["", "", "*", "0", " ", "-", "x"])
        s += rnd.choice("<>^" if kind == "s" else "<>^=")
    if kind != "s" and rnd.random() < 0.3:
        s += rnd.choice("+- ")
    if kind == "f" and rnd.random() < 0.1:
        s += "z"
    if kind != "s" and rnd.random() < 0.2:
        s += "#"
    if kind != "s" and rnd.random() < 0.2:
        s += "0"
    if rnd.random() < 0.5:
        s += str(rnd.randint(0, 15))
    if kind != "s" and rnd.random() < 0.25:
        s += rnd.choice([",", "_"])
    types = {"s": ["", "s"],
             "i": ["", "d", "b", "o", "x", "X", "c", "e", "f", "g", "%", "E", "F", "G"],
             "f": ["", "e", "E", "f", "F", "g", "G", "%"]}[kind]
    if kind == "i" and rnd.random() < 0.3:
        s += "." + str(rnd.randint(0, 5))
        types = ["e", "f", "g", "%", "E", "F", "G"]
    elif kind != "i" and rnd.random() < 0.4:
        s += "." + str(rnd.randint(0, 8))
    return s + rnd.choice(types)


with open(os.path.join(out, "format.tsv"), "w", encoding="utf-8") as f:
    n = 0
    while n < 3000:
        kind = rnd.choice("sifb")
        if kind == "s":
            v, sp = rnd.choice(STRS), spec("s")
        elif kind == "i":
            v, sp = rnd.choice(INTS + [rnd.randint(-10**9, 10**9)]), spec("i")
        elif kind == "b":
            v, sp = rnd.choice([True, False]), spec(rnd.choice("is"))
        else:
            v = rnd.choice(FLOATS + [rnd.uniform(-1e6, 1e6), rnd.uniform(-1, 1)])
            sp = spec("f")
        try:
            got = format(v, sp)
        except Exception as e:  # noqa: BLE001 -- the refusal is the answer
            got = "!" + type(e).__name__
        if any(ch in got for ch in "\t\n\r"):
            continue
        val = repr(v) if kind == "f" else str(v)
        f.write(f"{kind}\t{val}\t{sp}\t{got}\n")
        n += 1

with open(os.path.join(out, "str.tsv"), "w", encoding="utf-8") as f:
    xs = [0.0, -0.0, 1.0, 1e15, 1e16, 9999999999999998.0, 1e-4, 1e-5, 0.1,
          1 / 3, 2.5, 123456789012345.6, 1.5e300, 5e-324, math.inf,
          -math.inf, math.nan]
    while len(xs) < 1000:
        if rnd.random() < 0.5:
            bits = struct.pack("Q", rnd.getrandbits(63))
            xs.append(struct.unpack("d", bits)[0] * rnd.choice([1, -1]))
        else:
            xs.append(rnd.uniform(-1e6, 1e6) * 10 ** rnd.randint(-20, 20))
    for x in xs:
        f.write(f"{x!r}\t{x}\n")
