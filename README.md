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

## Quickstart

**Register a credential once.** We'll use an OpenAI key; paste your real key at
the prompt — it goes straight to your OS keychain, never a file:

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

Save this as `demo.py` (`pip install openai` first):

```python
import os
from openai import OpenAI

print("the key my code sees:", os.environ["OPENAI_API_KEY"])   # cloak-…, not the real one

reply = OpenAI().chat.completions.create(                      # OpenAI() reads the env cloak injected
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "In 5 words: what is a credential proxy?"}],
)
print("…and the call still works:", reply.choices[0].message.content)
```

Run it through cloak:

```console
$ cloak run -- python demo.py
the key my code sees: cloak-8f3a1c9d2e...
…and the call still works: A local secret-swapping middleman.
```

Your code's environment holds a **fake** key — yet the request succeeds,
because cloak swapped in the real one over verified TLS. No code change beyond
what you'd write anyway.

**Drop the `cloak run` prefix:** run `cloak start` once and cloak stays up as a
background service (starting at login). Then `python demo.py` — and anything
that reads a `.env` you've moved into cloak — just works. `cloak status` shows
what's live; `cloak stop` removes it.

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
