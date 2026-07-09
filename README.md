# Cloak

**Hand your AI agent a fake credential; keep the real one out of its context.**

An agent that queries your database or calls an LLM API needs a real
credential — and the moment it holds one, that secret is in its context
window, its logs, its observability traces, and any shared session. Those are
the places nobody rotates secrets out of.

Cloak is a tiny local proxy. It hands the agent a **fake** DSN or API key
pointing at `localhost` and swaps in the real one on the way out — over
verified TLS, loaded from your OS keychain. The agent never sees it.

Works today for **PostgreSQL** and **HTTP APIs** (OpenAI, Anthropic, and
anything that takes a bearer token or an API-key header).

## Install

```console
# macOS (Homebrew)
brew install hoophq/tap/cloak

# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/hoophq/cloak/main/install.sh | sh
```

Or a signed binary from [releases](https://github.com/hoophq/cloak/releases),
or `go install github.com/hoophq/cloak@latest` (Go 1.26+).

## In a conversational session

Register the upstream once, wire cloak into Claude Code, then just run
`claude` — no wrapper to remember.

```console
$ cloak add pg-prod --url postgres://app_user@prod-db.internal:5432/app --env DATABASE_URL
Password for app_user@prod-db.internal: ****

$ cloak init        # adds the fake credentials + session hooks to ~/.claude/settings.json
$ claude            # native — cloak is on, no `cloak run`
```

Inside the session the agent — and everything it spawns (a shell, `psql`, an
SDK) — sees only a fake, loopback DSN:

```
DATABASE_URL=postgres://cloak:2db1db61ef5ad177@127.0.0.1:5433/pg-prod?sslmode=disable
```

Cloak validates the token and connects to the real database with the keychain
credential. `cloak init` starts a small proxy when a session opens and stops it
when the last one closes; `cloak uninstall` removes it cleanly.

> No global config, or a one-off? `cloak run -- claude` does the same for a
> single session (and mints a fresh per-run token) — handy for CI and scripts.

## In an application

Same idea for an agentic backend (LangChain or any AI SDK). Register the
provider, run your service under Cloak, and read the injected environment in
code — the real API key never enters your app's process, logs, or LLM traces.

```console
$ cloak add openai --type http --host api.openai.com --auth bearer \
    --env OPENAI_API_KEY --env-url OPENAI_BASE_URL

$ cloak run -- python -m myagent.server
cloak: OPENAI_API_KEY, OPENAI_BASE_URL → openai (127.0.0.1:5434)
```

```python
import os
from langchain_openai import ChatOpenAI

# Both values come from Cloak — the fake key and the loopback URL.
llm = ChatOpenAI(
    model="gpt-4o",
    base_url=os.environ["OPENAI_BASE_URL"],  # http://127.0.0.1:5434
    api_key=os.environ["OPENAI_API_KEY"],    # cloak-<token>
)
```

Cloak swaps in the real key and forwards to `https://api.openai.com`; streaming
works unchanged. Anthropic and other API-key headers use `--auth
header:x-api-key`.

Don't want to prefix every run? `cloak start` runs cloak as an always-on
background service (starting at login). Then `python -m myagent.server` — and
anything that reads a `.env` you moved into cloak — just works, no wrapper.
`cloak status` shows what's live; `cloak stop` removes it. For containers, MCP
servers, and the `CLOAK_SECRET_KEY` deploy pattern, see the
[integration guide](docs/INTEGRATIONS.md).

## What it protects — and what it doesn't

Cloak keeps the real credential out of the agent's context, logs, and traces,
and a leaked fake credential is worthless off-box and dies with the session.

It does **not** stop a prompt-injected agent from *misusing* the live access
while the proxy runs — the fake credential is a working capability. Pair Cloak
with your agent's sandboxing and permissions: **it protects the credential,
not the access.** The [threat model](docs/THREAT_MODEL.md) is honest about the
boundaries — read it before you rely on it.

## Docs

- **[Integration guide](docs/INTEGRATIONS.md)** — Claude Code, MCP servers, agentic backends, CI.
- **[Threat model](docs/THREAT_MODEL.md)** — what it protects, what it doesn't, and why.
- **[FAQ](docs/FAQ.md)** — why not env vars or a secrets manager; how it differs from Agent Vault and Secretless.

Other commands: `cloak list`, `cloak rm <name>`, and `cloak import <file>` to
move credentials out of an existing `.env`. Run `cloak --help` for the rest.

## Development

```console
make build   # build the binary
make test    # unit tests
make e2e     # full broker path against real PostgreSQL (Docker)
```

## License

MIT
