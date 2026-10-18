# FlatBuffers test fixtures

Development-only fixtures for `internal/flatview` tests. Nothing here ships
with the binary; there is no runtime FlatBuffers dependency.

## Provenance

All BFBS schemas, JSON-built payloads and generated C++ headers were produced
by `flatc` built from the official FlatBuffers source pinned at commit
`03fffb25e2d777462b719cb4964249c30b19d58f` (tag
`v25.12.19-2026-02-06-03fffb2`), Apache-2.0. Source tarball SHA-256:

    dbed32bd1d0b484405778d80c7a5ba223a917c1f8eb5c062e97a915c52f04a06

    cmake -S <src> -B build -DCMAKE_BUILD_TYPE=Release \
      -DFLATBUFFERS_BUILD_TESTS=OFF -DFLATBUFFERS_BUILD_FLATLIB=OFF \
      -DFLATBUFFERS_BUILD_FLATHASH=OFF
    cmake --build build --target flatc -j 8      # flatc version 25.12.19
    # flatc binary SHA-256: 7651481ef1778a8f8885b0cad9c2c8afcfc1cc3e0c5b2997cc2e8afa7e23716d

Schemas and payloads were generated with relative paths from a scratch
directory (`flatc --binary --schema -o gen schemas/<name>.fbs` and
`flatc --binary -o gen schemas/<name>.fbs data/<name>.json`). The generated
artifacts were inspected for absolute workstation paths before being copied
here; none are present. The schemas live with the oracle program sources
during development and are reproduced below for reference.

Wide/sparse/negative union discriminators, string and struct union members,
shared payload objects and required-union payloads cannot be produced through
flatc's JSON path at this pin (it writes union discriminators one byte wide,
corrupting wider tags; see the failed-attempt note below). Those payloads were
built with the generated C++ builder API (`cc/oracle_build_wide.cc`,
`cc/oracle_required.cc`, `cc/oracle_negzero.cc`), compiled against the pinned
runtime headers, or assembled byte-by-byte and then verified with the
independent generated C++ verifier and accessors (`cc/oracle_dump.cc`).

`ptstruct.bin` is hand-assembled: the pinned C++ builder API has no supported
route for a standalone struct-union-member offset (two attempts produced
buffers whose resolved union value did not carry the written struct bytes;
those attempts are retained in the private verification log). The committed
bytes pass the generated `VerifyRecordBuffer` and the generated accessor
reads the intended `{a:-5, b:65500}`.

## Files

| File | Kind | Notes |
| --- | --- | --- |
| `profile.bfbs` | schema | Full coverage schema: all scalar widths, bool, floats, enums (ubyte/long/ulong underlying), wide `Body : ulong` and signed `Side : short` unions with sparse/aliased members, string/struct/table union members, optional scalars, defaults (including `-0.0` and `18446744073709551615`), deprecated field, force-aligned `Vec3` struct, recursive `Node` table, scalar/string/table/struct vectors, `[ubyte]` bytes, file identifier `NTST`. |
| `full.bin` | payload | Present values, float32 promotion, payload-stored `-0`, unknown enum value 77, present-empty vector, recursion, bytes. |
| `none.bin`, `defaults.bin` | payload | Absent fields exercising defaults, optional omission and NONE tags. |
| `wide.bin`, `echo.bin` | payload | C++-built wide table-union tags 300 and aliased 400. |
| `left.bin` | payload | C++-built negative signed 16-bit discriminator `-3`. |
| `ptstruct.bin` | payload | Hand-assembled struct union member; negative vtable displacement. |
| `textstr.bin` | payload | C++-built string union member (tag 5). |
| `shared.bin` | payload | C++-built shared Inner object referenced twice. |
| `negzero.bfbs` | payload+schema | `.fbs` declares `x:double = -0.0; y:float = -0.0`; the compiler omits both stored defaults, so absent fields read as `0` here while the original-schema accessor yields `-0`. |
| `negzero_stored.bin` | payload | Built via `CreateNeg(fbb, -0.0, -0.0f)`: the builder compares `-0.0 == 0.0` and stores nothing; documents builder-level default equality. |
| `negzero2.bfbs` | payload+schema | Metadata-stored negative zero via declared-width narrowing: `z:float = -1e-50` stores a nonzero double default that narrows to `-0.0f`; `w` narrows to `+0`; `d:double = -1e-320` keeps its subnormal value. |
| `required.bfbs` | schema | Recursive required union `u: U (required)` with `U : ushort` members (tags 2 and 260). |
| `req_ok.bin`, `req_bad_none.bin` | payload | C++-built: valid tag-2 value, and tag NONE with the required value slot populated. The generated verifier accepts the latter; this reader rejects it per the stricter NONE contract. |
| `nested.bfbs`, `nested.bin` | schema+payload | `struct Inner { b: long; } struct Outer { a: int32; inner: Inner; } table Root { s: Outer; }` with `s = {a:5, inner:{b:42}}`: struct-inside-struct nesting, generated through the ordinary flatc JSON path. Pins the reference-resolution ordering regression found in review. |
| `ulongvec.bfbs` | schema | `table Root { vals: [ulong]; }`, flatc-generated; used with an in-test hand-built payload proving 64-bit vector elements charge decimal text against the 16 MiB view budget. |

Expected view values in the tests were cross-checked against
`cc/oracle_dump.cc` output from the pinned generated C++ accessors, not
against reflection reads or regenerated BFBS.
