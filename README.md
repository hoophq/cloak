# Cloak

Hand your AI agent a fake credential; keep the real one out of its context.

Cloak is a tiny local proxy. You register an upstream once — the real
credential goes into your OS keychain — and run your agent through Cloak.
The agent gets a fake DSN pointing at localhost, and Cloak swaps in the real
credential on the way out. The real secret never enters the agent's context
window, logs, or traces.

```console
$ cloak add pg-prod --url postgres://app_user@prod-db.internal:5432/app --env DATABASE_URL
Password for app_user@prod-db.internal: ****
✓ pg-prod registered (credential in OS keychain)

$ cloak run -- claude
cloak: DATABASE_URL → pg-prod (127.0.0.1:5433)
```

Inside the session, the agent sees only:

```
DATABASE_URL=postgres://cloak:2db1db61ef5ad177@127.0.0.1:5433/app?sslmode=disable
```

That token is random, minted per `cloak run`, and useless from any other
machine — or from the same machine once the session ends.

## How it works

1. `cloak run` binds a loopback listener per upstream and injects fake DSNs
   as environment variables into the command it wraps.
2. The agent (or anything it spawns — `psql`, drivers, scripts) connects to
   the listener and authenticates with the fake token.
3. Cloak opens the real connection — TLS with full verification, then
   SCRAM-SHA-256 / md5 / cleartext auth with the credential from the OS
   keychain — and from then on splices bytes transparently.

## What it protects — and what it doesn't

**Protects against:** the real credential leaking into the agent's context
window, transcripts, LLM observability traces, shared sessions, shell
history, and logs — the places nobody rotates secrets out of. A leaked fake
DSN is worthless off-box and expires with the session.

**Does not protect against:** a prompt-injected agent *misusing* the access
itself — the fake DSN is a live capability while the proxy runs. Pair Cloak
with your agent's sandboxing and permission controls for that half. It is
also not a defense against a hostile process with full local shell access.

## Commands

| Command | What it does |
|---|---|
| `cloak add <name>` | Register an upstream; password prompted, stored in the OS keychain |
| `cloak list` | Show registered upstreams (never credentials) |
| `cloak run -- <cmd>` | Run a command with fake DSNs injected; proxy for the session |
| `cloak rm <name>` | Remove an upstream and its keychain entry |

Supported upstreams: PostgreSQL (SCRAM-SHA-256, md5, cleartext; TLS
`verify-full` by default). More protocols are planned.

## Development

```console
make build   # build the cloak binary
make test    # unit tests
make e2e     # full broker path against real PostgreSQL (requires Docker)
```

## License

Apache-2.0
