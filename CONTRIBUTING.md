# contributing to spermacore

thank you for taking time to read this file. we welcome patches and contributions.

## getting started

1. check whether there is already an open issue or pull request covering your topic.
2. for large architectural changes, open an issue first to discuss your approach. that way you avoid spending hours on code that might not fit the core architecture.
3. small fixes - typos, test improvements, docs - can be submitted directly.

## contribution license

by submitting a pull request, you agree that your contribution is licensed under the repository license - [PolyForm Noncommercial 1.0.0](LICENSE). you confirm that you have the right to submit this code and that it does not infringe third-party rights.

if your contribution contains third-party code, specify its origin and license in the pull request description. code without a compatible license cannot be merged.

## commit signatures

we use simple dco sign-offs. each commit should contain a signed-off line:

```text
Signed-off-by: Name <email@example.com>
```

you can add it automatically:

```bash
git commit -s -m "fix: concise description"
```

## environment requirements

| component | version |
|---|---|
| go | 1.22+ |
| gcc (mingw-w64 on windows) | modern gcc |
| cmake | 3.20+ |
| curl | on posix |

## checks before sending a pr

go:

```bash
gofmt -l core modules
go vet ./...
go test ./...
go test -race ./...
go mod tidy -diff
```

c/c++ modules:

```bash
cmake -S . -B build -G Ninja
cmake --build build
```

builds compile with `-Wall -Wextra -Werror` (gcc) and `/W4 /WX` (msvc). warnings are not suppressed - code is fixed rather than disabling flags. the single exception is `-Wmissing-field-initializers` for c++, since c++20 permits omitting trailing fields in designated initializers and gcc warns about it.

if you modify ci, ensure both ubuntu and windows runners remain green.

## code style

**go**

- standard `gofmt` without exceptions.
- errors handled explicitly; `panic` only where recovery is impossible.
- no hidden global state; dependencies passed via configuration and structs.
- tuning parameters live in `config.toml`, not in code.

**c/c++**

- c11 for legacy compatibility, c++20 for modern modules; core remains pure go.
- structure initializers use designated initializers.
- use `SC_THREAD_LOCAL` for thread-local buffers, not raw `_Thread_local` or `thread_local`.
- exports like `plugin_get_api` stay in `extern "C"`; headers already wrap contracts.
- struct field `requires` in `sc_plugin_info_t` is named `requires_plugins` (`requires` is a c++20 keyword).
- memory is freed where ownership is defined. see `include/plugin_api.h` and `docs/docs_commits/` for `out_json` contract.
- avoid `/* ... */` comments inside c-preamble in `core/cgo_bridge.go`.

**abi**

`include/plugin_api.h` is the public contract. breaking changes require bumping the abi version, updating all modules, and documenting changes in `docs/docs_commits/`.

## configuration

`config.toml` is meant to be human-readable. preserve comments, section order, and formatting. do not wipe existing sections when changing one key.

## commits

one logical change per commit. avoid mixing refactoring with bug fixes.

message format:

```text
type(scope): concise imperative summary
```

types: `feat`, `fix`, `docs`, `test`, `ci`, `refactor`, `chore`.

examples:

```text
feat(grouper): honor grouper_time before window_ms
fix(webui): reject unauthenticated websocket upgrades
docs: rewrite readme in concise english
```

## adding a plugin

1. create `modules/<name>/src/<name>.cpp` (c++20) or `.c` (c11).
2. implement `plugin_get_api()` and populate `sc_plugin_info_t`.
3. expose tools via `get_tools()` and handle invocations in `call_tool()`.
4. set `SC_PLUGIN_FLAG_THREAD_SAFE` only if the plugin safely synchronizes state.
5. specify `out_json` ownership: internal buffer or `SC_PLUGIN_FLAG_OWNS_TOOL_RESULT`.
6. add module entry to `config.toml` and `project-tree.md`.
7. compile with `-Werror` and verify clean loading by the core runtime.

## what we will not accept

- secrets, credentials, api tokens, or filled-in `config.toml` files.
- precompiled binary blobs (`.dll`, `.so`, `.exe`) in PRs.
- patches silencing compiler warnings without fixing the root cause.
- undocumented abi changes.
- massive unsolicited reformatting of unrelated files.

## security

please do not file public issues for security vulnerabilities. report them privately via github security advisories or directly to project maintainers. allow reasonable time for a fix before disclosing details.

## documentation

when changing behavior, config flags, or abi:
- update `README.md`;
- add a note to `docs/docs_commits/`;
- update `project-tree.md` if folder structure changes.
