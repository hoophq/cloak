# Cloak

Local credential proxy: hands AI agents fake DSNs / API keys and swaps in the
real secret on egress, so credentials never enter agent context, logs, or
traces. Single Go binary (Go 1.26). See README.md; guarantees and limits in
docs/THREAT_MODEL.md.

## Commands
- `make build` · `make test` · `make vet` — build, unit tests, go vet
- `make e2e` — broker path against real Postgres (Docker; `-tags e2e`)
- e2e tests live behind `-tags e2e`, so `go build ./...` / `go test ./...`
  skip them. After changing a shared interface, run `go build -tags e2e ./...`
  to catch e2e breakage.

## Constraints
- **Standalone**: no hard dependency on any hoop codebase — protocol handling
  is written from scratch (the Postgres wire codec in `internal/pgwire` is
  hand-rolled).
- **Minimal dependencies**: prefer the stdlib and hand-roll to avoid a dep
  (e.g. `crypto/pbkdf2` over `golang.org/x/crypto`).
- `internal/config` holds no secrets — it references them by upstream name.
  Real secrets live in the OS keychain or an encrypted file (`internal/secret`).

## Conventions
- **Honest output**: user-facing messages and docs must match actual behavior
  (name the real backend; don't oversell in the threat model).
- **Security**: constant-time token compares (`crypto/subtle`); loopback-only
  listeners; secrets never in argv, logs, or config.
- Table-driven tests; `t.Setenv` + `t.TempDir` for XDG isolation; `httptest`
  for HTTP, Docker for Postgres e2e.
