# Cloak

Hand your AI agent a fake credential; keep the real one out of its context.

Cloak is a tiny local proxy. You register an upstream once — the real
credential goes into your OS keychain — and run your agent through Cloak.
The agent gets a fake DSN or API key pointing at localhost, and Cloak swaps
in the real credential on the way out. The real secret never enters the
agent's context window, logs, or traces.

Works today for **PostgreSQL** databases and **HTTP APIs** (OpenAI,
Anthropic, Stripe, GitHub, your internal services — anything that takes a
bearer token or an API-key header).

```console
$ cloak add pg-prod --url postgres://app_user@prod-db.internal:5432/app --env DATABASE_URL
Password for app_user@prod-db.internal: ****
✓ pg-prod registered (credential in OS keychain)

$ cloak run -- claude
cloak: DATABASE_URL → pg-prod (127.0.0.1:5433)
```

Inside the session, the agent sees only:

```
DATABASE_URL=postgres://cloak:2db1db61ef5ad177@127.0.0.1:5433/pg-prod?sslmode=disable
```

That token is random, minted per `cloak run`, and useless from any other
machine — or from the same machine once the session ends.

For an HTTP API it looks the same, but the agent gets a fake key and a
loopback base URL:

```console
$ cloak add openai --type http --host api.openai.com --auth bearer \
    --env OPENAI_API_KEY --env-url OPENAI_BASE_URL
Secret for api.openai.com: ****

$ cloak run -- claude
cloak: OPENAI_API_KEY, OPENAI_BASE_URL → openai (127.0.0.1:5434)
```

The SDK reads `OPENAI_BASE_URL=http://127.0.0.1:5434` and
`OPENAI_API_KEY=cloak-<token>`; Cloak swaps in the real key and forwards to
`https://api.openai.com`. For a header-based API use `--auth header:x-api-key`
(e.g. Anthropic).

## How it works

1. `cloak run` binds a loopback listener per upstream and injects the fake
   DSN/key (and, for HTTP, a loopback base URL) as environment variables
   into the command it wraps.
2. The agent (or anything it spawns — `psql`, an SDK, `curl`, a script)
   connects to the listener and presents the fake token.
3. Cloak validates the token, then reaches the real upstream — TLS with full
   verification — with the credential from the OS keychain:
   - **Postgres:** SCRAM-SHA-256 / md5 / cleartext auth, then a transparent
     byte splice for the rest of the session.
   - **HTTP:** the real credential is injected into the configured header
     (`Authorization: Bearer …` or a named header) and the request is
     forwarded; streaming responses pass straight through.

## What it protects — and what it doesn't

**Protects against:** the real credential leaking into the agent's context
window, transcripts, LLM observability traces, shared sessions, shell
history, and logs — the places nobody rotates secrets out of. A leaked fake
DSN is worthless off-box and expires with the session.

**Does not protect against:** a prompt-injected agent *misusing* the access
itself — the fake DSN is a live capability while the proxy runs. Pair Cloak
with your agent's sandboxing and permission controls for that half. It is
also not a defense against a hostile process with full local shell access.

Cloak protects the *credential*, not the *access*. Read the full
[threat model](docs/THREAT_MODEL.md) before you rely on it — the honesty is
the point.

## Importing an existing .env

A fake DSN next to a real one in `.env` protects nothing — the agent reads
the file anyway. `cloak import` moves the real credential out:

```console
$ cloak import .env
⚠ OPENAI_API_KEY (line 3): credential-shaped value; cloak cannot proxy this yet
→ DATABASE_URL (line 2): upstream "database-url" on 127.0.0.1:5433, password moves to the OS keychain
Rewrite .env? [y/N] y
✓ imported 1 credential(s); .env rewritten (original backed up)
```

The entry keeps its variable name but now holds only a placeholder pointing
at the cloak listener; the original file is backed up outside the project
tree (`cloak import --undo .env` restores it). Values cloak can't proxy yet
are flagged so you know what still leaks.

## Commands

| Command | What it does |
|---|---|
| `cloak add <name>` | Register an upstream; password prompted, stored in the OS keychain |
| `cloak import [file]` | Move credentials out of a .env file into cloak |
| `cloak list` | Show registered upstreams (never credentials) |
| `cloak run -- <cmd>` | Run a command with fake DSNs injected; proxy for the session |
| `cloak rm <name>` | Remove an upstream and its keychain entry |

Supported upstreams:

- **PostgreSQL** — SCRAM-SHA-256, md5, cleartext; TLS `verify-full` by default.
- **HTTP APIs** — bearer token or named header (`--auth bearer` /
  `--auth header:<name>`); reverse-proxy on a loopback port.

More protocols are planned.

## Docs

- **[Integration guides](docs/INTEGRATIONS.md)** — Claude Code, MCP servers,
  Cursor, and plain scripts, with verification steps and gotchas.
- **[Threat model](docs/THREAT_MODEL.md)** — what Cloak protects, what it
  doesn't, and the design choices that back the claims.
- **[FAQ](docs/FAQ.md)** — why not env vars / a secrets manager, how it
  differs from Agent Vault and Secretless, and whether the fake DSN is a
  boundary (it isn't).

## Development

```console
make build   # build the cloak binary
make test    # unit tests
make e2e     # full broker path against real PostgreSQL (requires Docker)
```

## License

Apache-2.0
