"""Independently decode finite actual-CLI conversion outputs.

Use the verified, uninstalled msgpack 1.1.1 source on PYTHONPATH. Each decode
uses a fresh fallback.unpackb, never failed streaming state or a native module.
See README.md for provenance and advisory containment. This is not a hostile
input verifier or a claim that the oracle dependency is vulnerability-free.
"""
import argparse
import base64
import hashlib
import json
import math
from pathlib import Path
import struct
import subprocess

import msgpack
from msgpack import fallback
from msgpack.ext import Timestamp


class Pairs(list):
    """Retain ordered, duplicate and compound map keys."""


class Extension:
    def __init__(self, code, data):
        self.code, self.data = code, data


def scalar_float(value):
    return ("float", "NaN" if math.isnan(value) else struct.pack(">d", value).hex())


def decoded(value):
    if value is None or isinstance(value, (str, bool)):
        if isinstance(value, str):
            return ("str", value.encode("utf-8", "surrogateescape"))
        return (type(value).__name__, value)
    if isinstance(value, Pairs):
        return ("map", [(decoded(k), decoded(v)) for k, v in value])
    if isinstance(value, list):
        return ("array", [decoded(v) for v in value])
    if isinstance(value, int):
        return ("int", value)
    if isinstance(value, float):
        return scalar_float(value)
    if isinstance(value, bytes):
        return ("bin", value)
    if isinstance(value, Timestamp):
        return ("time", value.seconds, value.nanoseconds)
    if isinstance(value, Extension):
        return ("ext", value.code, value.data)
    raise AssertionError(type(value))


def expected(value):
    if isinstance(value, list):
        return ("array", [expected(v) for v in value])
    if not isinstance(value, dict):
        return decoded(value)
    key, arg = next(iter(value.items()))
    if key == "$map":
        return ("map", [(expected(k), expected(v)) for k, v in arg])
    if key == "$int":
        return ("int", int(arg))
    if key == "$float":
        return scalar_float(float(arg))
    if key in ("$bin", "$str64"):
        return ("bin" if key == "$bin" else "str", base64.b64decode(arg, validate=True))
    if key == "$ext":
        return ("ext", arg[0], base64.b64decode(arg[1], validate=True))
    if key == "$time":
        return ("time", int(arg[0]), arg[1])
    raise AssertionError(key)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output-dir", required=True, type=Path)
    args = parser.parse_args()
    assert msgpack.version == (1, 1, 1)
    binary = args.binary.resolve()
    args.output_dir.mkdir(parents=True, exist_ok=False)
    fixtures = json.loads(Path(__file__).with_name("corpus.json").read_text())
    receipts = []

    def check(name, codec, source, want, extra=()):
        src = args.output_dir / (name + ".input")
        src.write_bytes(source)
        before = hashlib.sha256(source).hexdigest()
        outputs = []
        for mode in ("file", "stdin"):
            dst = args.output_dir / (name + "." + mode + ".msgpack")
            # Shell owns redirection, always to a distinct path. Fixed script;
            # filenames/codec/extra options are positional data, not shell text.
            if mode == "file":
                script = 'bin=$1; src=$2; dst=$3; codec=$4; shift 4; exec "$bin" --input-file "$src" --input-codec "$codec" --to msgpack "$@" > "$dst"'
            else:
                script = 'bin=$1; src=$2; dst=$3; codec=$4; shift 4; exec "$bin" --input-stdin --input-codec "$codec" --to msgpack "$@" < "$src" > "$dst"'
            result = subprocess.run(["/bin/sh", "-c", script, "sh", str(binary), str(src),
                                     str(dst), codec, *extra], capture_output=True, timeout=10, check=False)
            assert result.returncode == 0 and result.stdout == b"" and result.stderr == b"", (name, mode, result)
            wire = dst.read_bytes()
            # A fresh one-shot unpacker rejects extra values or trailing LF;
            # rich normalization keeps float bits/sign, map pairs, str/bin apart.
            value = fallback.unpackb(wire, raw=False, unicode_errors="surrogateescape",
                                     strict_map_key=False, object_pairs_hook=Pairs,
                                     ext_hook=Extension, timestamp=0)
            assert decoded(value) == expected(want), (name, mode, decoded(value), expected(want))
            assert hashlib.sha256(src.read_bytes()).hexdigest() == before
            outputs.append(wire)
            receipts.append(dict(name=name, codec=codec, mode=mode, source_sha256=before,
                                 output_sha256=hashlib.sha256(wire).hexdigest(), size=len(wire)))
        assert outputs[0] == outputs[1], name

    for fixture in fixtures:
        check(fixture["name"] + "-wire", "msgpack", bytes.fromhex(fixture["hex"]), fixture["view"])
        check(fixture["name"] + "-rich", "msgpack-json", json.dumps(fixture["view"]).encode(), fixture["view"])
    for name, source, want in [
        ("plain-collision", '{"$int":"7"}', {"$map": [["$int", "7"]]}),
        ("plain-order", '{"z":1,"a":2}', {"$map": [["z", {"$int": "1"}], ["a", {"$int": "2"}]]}),
        ("plain-numbers", '[0.1,1e3,1000,-0,-0.0,-0e3,5e-324,18446744073709551615,-9223372036854775808]',
         [{"$float": "0.1"}, {"$float": "1000"}, {"$int": "1000"}, {"$float": "-0"},
          {"$float": "-0"}, {"$float": "-0"}, {"$float": "5e-324"},
          {"$int": "18446744073709551615"}, {"$int": "-9223372036854775808"}]),
    ]:
        check(name, "json", source.encode(), want, ("--filter", "true"))
    check("plain-selected", "json", b'{"bad":1e309,"ok":7}', {"$int": "7"}, ("--project", ".ok"))
    check("rich-selected", "msgpack-json", b'{"$map":[["k",{"$bin":"AP8="}]]}',
          {"$bin": "AP8="}, ("--project", '."$map"[0][1]'))
    print(json.dumps(dict(binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
                         backend=str(Path(fallback.__file__).resolve()),
                         backend_sha256=hashlib.sha256(Path(fallback.__file__).read_bytes()).hexdigest(),
                         checks=len(receipts), receipts=receipts), indent=2))


if __name__ == "__main__":
    main()
