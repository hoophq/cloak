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

**macOS (Homebrew):**

```console
brew install hoophq/tap/cloak
```

**macOS / Linux (install script):**

```console
curl -fsSL https://raw.githubusercontent.com/hoophq/cloak/main/install.sh | sh
```

Or a signed binary from [releases](https://github.com/hoophq/cloak/releases),
or `go install github.com/hoophq/cloak@latest` (Go 1.26+).

## Quickstart

**Point `cloak import` at your `.env` — that's the whole setup.** Say it holds a
database URL and an OpenAI key:

```console
$ cat .env
DATABASE_URL=postgres://app:s3cr3t@prod-db.internal:5432/app
OPENAI_API_KEY=sk-proj-abc123...
```

```console
$ cloak import .env
→ DATABASE_URL (line 1): upstream "database-url" on 127.0.0.1:5433, credential moves to the OS keychain
→ OPENAI_API_KEY (line 2): upstream "openai-api-key" on 127.0.0.1:5434, credential moves to the OS keychain
Rewrite .env? [y/N] y
✓ imported 2 credential(s); .env rewritten (original backed up)
```

Both secrets are now in your keychain, and the `.env` reads back **fakes** — a
loopback DSN and a `cloak-…` key that only resolve through cloak:

```console
$ cat .env
DATABASE_URL=postgres://cloak:…@127.0.0.1:5433/database-url?sslmode=disable
OPENAI_API_KEY=cloak-…
OPENAI_BASE_URL=http://127.0.0.1:5434
```

cloak recognizes Postgres DSNs and common LLM providers (OpenAI, Anthropic) on
sight. Undo any time with `cloak import --undo .env`.

**Need more control?** `cloak add` registers one upstream by hand — the escape
hatch for a provider import doesn't know yet, a custom host, or a non-standard
env name. Same keychain, same fakes; paste the real secret at the prompt:

```console
$ cloak add openai --type http --host api.openai.com --auth bearer \
    --env OPENAI_API_KEY --env-url OPENAI_BASE_URL
Secret for api.openai.com: ****
```

Now pick your path.

### A · A conversational agent (Claude Code)

```console
$ cloak init      # wire cloak into Claude Code — one time
$ claude          # a 🔒 banner confirms cloak is on
```

**See it work:** ask the agent to print its own key —

> print the value of `$OPENAI_API_KEY`

It answers `cloak-…`, never your real key. Everything it spawns — a shell, an
SDK, `curl` — sees the same fake, while requests still reach OpenAI with the
real key swapped in on the way out. Undo any time with `cloak uninstall`.

### B · Your own application

Save this as `demo.py` (`pip install openai psycopg python-dotenv`):

```python
import os, psycopg
from dotenv import load_dotenv
from openai import OpenAI

load_dotenv()   # your app already does this — now it loads cloak's fakes

# Every value the code sees is fake.
print("DB URL: ", os.environ["DATABASE_URL"])     # postgres://cloak:…@127.0.0.1
print("API key:", os.environ["OPENAI_API_KEY"])   # cloak-…

# …yet the query still reaches your real database over verified TLS.
with psycopg.connect(os.environ["DATABASE_URL"]) as db:
    print("connected as:", db.execute("select current_user").fetchone()[0])

# …and the API call still reaches OpenAI with your real key swapped in.
reply = OpenAI().chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "In 5 words: what is a credential proxy?"}],
)
print("openai says:", reply.choices[0].message.content)
```

**Set it up once, then forget it.** Start cloak as a background service — it
comes back at login — and run your app exactly the way you always have. No
prefix, no wrapper, nothing to remember:

```console
$ cloak start          # once; cloak now runs in the background
$ python demo.py
DB URL:  postgres://cloak:5f3bd32…@127.0.0.1:5433/database-url?sslmode=disable
API key: cloak-5f3bd32…
connected as: app
openai says: A local secret-swapping middleman.
```

That's the whole point: cloak runs without you noticing. Your code's
environment holds only **fakes** — a loopback DSN and a `cloak-…` key — yet
both the query and the request succeed, because cloak swapped in the real
credentials over verified TLS. `cloak status` shows what's live; `cloak stop`
tears it down.

**Just need a one-off run?** Skip the background service and wrap a single
command — cloak serves it for that run only, then exits:

```console
$ cloak run -- python demo.py
```

> Using LangChain or another framework? It delegates to the same SDKs, so it
> works the same way — see the [integration guide](docs/INTEGRATIONS.md) for
> LangChain, MCP servers, containers, and CI.

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

Other commands: `cloak list`, `cloak rm <name>`, `cloak status`. Run
`cloak --help` for the rest.

## Development

```console
make build   # build the binary
make test    # unit tests
make e2e     # full broker path against real PostgreSQL (Docker)
```

## License

MIT
