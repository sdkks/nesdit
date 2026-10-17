"""Generate the finite inspection corpus with msgpack 1.1.1's Python backend.

Run with that hash-verified source on PYTHONPATH; no installation is needed.
See README.md for provenance and the advisory mitigation. Output is JSON.
"""
import base64
import hashlib
import json
import math
import struct

import msgpack
from msgpack import fallback
from msgpack.ext import ExtType, Timestamp

assert msgpack.version == (1, 1, 1)


class Pairs(list):
    """Distinguish a map's ordered pairs from an ordinary array."""


class Extension:
    def __init__(self, code, data):
        self.code, self.data = code, data


def b64(data):
    return base64.b64encode(data).decode("ascii")


def view(value):
    if value is None or isinstance(value, bool):
        return value
    if isinstance(value, Pairs):
        return {"$map": [[view(k), view(v)] for k, v in value]}
    if isinstance(value, list):
        return [view(v) for v in value]
    if isinstance(value, str):
        raw = value.encode("utf-8", "surrogateescape")
        try:
            raw.decode("utf-8", "strict")
        except UnicodeDecodeError:
            return {"$str64": b64(raw)}
        return value
    if isinstance(value, bytes):
        return {"$bin": b64(value)}
    if isinstance(value, int):
        return {"$int": str(value)}
    if isinstance(value, float):
        if math.isnan(value):
            text = "NaN"
        elif math.isinf(value):
            text = "Infinity" if value > 0 else "-Infinity"
        elif value == 0:
            text = "-0" if math.copysign(1, value) < 0 else "0"
        else:
            text = repr(value)
        return {"$float": text}
    if isinstance(value, Timestamp):
        return {"$time": [str(value.seconds), value.nanoseconds]}
    if isinstance(value, Extension):
        return {"$ext": [value.code, b64(value.data)]}
    raise AssertionError(type(value))


cases = []


def add(name, wire, expected, producer="Python Packer"):
    # Fresh pure-Python Unpacker on every call, including error paths. Never
    # reuse streaming state after an error (GHSA-6v7p-g79w-8964).
    decoded = fallback.unpackb(
        wire, raw=False, unicode_errors="surrogateescape", strict_map_key=False,
        object_pairs_hook=Pairs, ext_hook=Extension, timestamp=0,
    )
    assert view(decoded) == expected, (name, view(decoded), expected)
    cases.append(dict(name=name, hex=wire.hex(), view=expected, producer=producer,
                      sha256=hashlib.sha256(wire).hexdigest()))


packer = fallback.Packer(use_bin_type=True, unicode_errors="surrogateescape")
for name, value, expected in [
    ("nil", None, None), ("false", False, False), ("true", True, True),
    ("utf8", "\ufeffhéllo\x00\r\n", "\ufeffhéllo\x00\r\n"),
    ("empty-string", "", ""), ("empty-array", [], []),
    ("empty-map", {}, {"$map": []}),
    ("int-min", -(2**63), {"$int": "-9223372036854775808"}),
    ("int-max", 2**64-1, {"$int": "18446744073709551615"}),
    ("integer-zero", 0, {"$int": "0"}),
    ("float64", 0.1, {"$float": "0.1"}),
    ("float64-subnormal", 5e-324, {"$float": "5e-324"}),
    ("float64-max", float.fromhex("0x1.fffffffffffffp+1023"),
     {"$float": "1.7976931348623157e+308"}),
    ("negative-zero", -0.0, {"$float": "-0"}),
    ("positive-zero", 0.0, {"$float": "0"}),
    ("nan", math.nan, {"$float": "NaN"}),
    ("infinity", math.inf, {"$float": "Infinity"}),
    ("negative-infinity", -math.inf, {"$float": "-Infinity"}),
    ("binary", b"\x00\xff", {"$bin": "AP8="}),
    ("empty-bin", b"", {"$bin": ""}),
    ("invalid-str", "\udcff", {"$str64": "/w=="}),
    ("opaque", ExtType(7, b"\x00\xff"), {"$ext": [7, "AP8="]}),
    ("collision", {"$int": "7"}, {"$map": [["$int", "7"]]}),
    ("array", [None, True, 1], [None, True, {"$int": "1"}]),
]:
    add(name, packer.pack(value), expected)

add("float32", fallback.Packer(use_single_float=True).pack(0.1),
    {"$float": "0.10000000149011612"})
add("pairs", packer.pack_map_pairs([
    ("name", "Ada"), ("name", "Grace"), ([1, "x"], b"\x00\xff"),
    ({"$int": "7"}, None),
]), {"$map": [["name", "Ada"], ["name", "Grace"],
               [[{"$int": "1"}, "x"], {"$bin": "AP8="}],
               [{"$map": [["$int", "7"]]}, None]]}, "Python pack_map_pairs")

# Explicit protocol fixtures exercise nonminimal headers and signed-positive
# opcodes that Packer does not select. Expectations are literal, not Go output.
for name, wire, expected in [
    ("signed-positive8", "d007", {"$int": "7"}),
    ("signed-positive16", "d10007", {"$int": "7"}),
    ("signed-positive32", "d200000007", {"$int": "7"}),
    ("signed-positive64", "d30000000000000007", {"$int": "7"}),
    ("fix-positive", "07", {"$int": "7"}),
    ("uint8", "ccff", {"$int": "255"}),
    ("uint16", "cdffff", {"$int": "65535"}),
    ("uint32", "ceffffffff", {"$int": "4294967295"}),
    ("negative-fix", "ff", {"$int": "-1"}),
    ("int8", "d080", {"$int": "-128"}),
    ("int16", "d18000", {"$int": "-32768"}),
    ("int32", "d280000000", {"$int": "-2147483648"}),
    ("str8", "d90161", "a"), ("str16", "da000161", "a"),
    ("str32", "db0000000161", "a"),
    ("bin16", "c50001ff", {"$bin": "/w=="}),
    ("bin32", "c600000001ff", {"$bin": "/w=="}),
    ("array16", "dc0001c0", [None]), ("array32", "dd00000001c0", [None]),
    ("map16", "de0001c0c3", {"$map": [[None, True]]}),
    ("map32", "df00000001c0c3", {"$map": [[None, True]]}),
    ("float32-subnormal", "ca00000001", {"$float": "1.401298464324817e-45"}),
    ("float32-negative-zero", "ca80000000", {"$float": "-0"}),
    ("float32-nan", "ca7fc00001", {"$float": "NaN"}),
    ("float32-infinity", "ca7f800000", {"$float": "Infinity"}),
    ("float32-negative-infinity", "caff800000", {"$float": "-Infinity"}),
]:
    add(name, bytes.fromhex(wire), expected, "explicit protocol bytes")

for code in range(-128, 128):
    if code != -1:
        add(f"ext-id-{code}", b"\xd4" + struct.pack("b", code) + b"\xff",
            {"$ext": [code, "/w=="]}, "explicit signed-ID protocol bytes")
for n in [0, 1, 2, 4, 8, 16, 17]:
    payload = b"x" * n
    add(f"ext-size-{n}", packer.pack(ExtType(0, payload)), {"$ext": [0, b64(payload)]})
    for prefix in [b"\xc7" + struct.pack("B", n), b"\xc8" + struct.pack(">H", n),
                   b"\xc9" + struct.pack(">I", n)]:
        add(f"ext-header-{prefix.hex()}", prefix + b"\x80" + payload,
            {"$ext": [-128, b64(payload)]}, "explicit extension header")

for seconds, nanos in [(0, 0), (2**32-1, 0), (2**34-1, 999999999),
                       (-1, 999999999), (-(2**63), 0), (2**63-1, 999999999)]:
    ts = Timestamp(seconds, nanos)
    expected = {"$time": [str(seconds), nanos]}
    add(f"timestamp-{seconds}-{nanos}", packer.pack(ts), expected)
    payload = ts.to_bytes()
    for prefix in [b"\xc7" + struct.pack("B", len(payload)),
                   b"\xc8" + struct.pack(">H", len(payload)),
                   b"\xc9" + struct.pack(">I", len(payload))]:
        add(f"timestamp-{seconds}-{nanos}-{prefix.hex()}", prefix + b"\xff" + payload,
            expected, "Python timestamp payload plus explicit extension header")
# Equivalent longer timestamp layouts, not only shortest producer choices.
for payload in [struct.pack(">Q", 7), struct.pack(">Iq", 0, 7)]:
    add(f"timestamp-long-{len(payload)}", b"\xc7" + bytes([len(payload), 255]) + payload,
        {"$time": ["7", 0]}, "explicit equivalent timestamp layout")

print(json.dumps(cases, ensure_ascii=False, indent=2, allow_nan=False))
