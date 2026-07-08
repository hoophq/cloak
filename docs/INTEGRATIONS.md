# Integration guides

## The one idea

`cloak run -- <command>` injects **fake** credentials as environment
variables into that command and everything it spawns, then proxies the real
connection for the life of the process:

```console
$ cloak run -- claude
cloak: DATABASE_URL → pg-prod (127.0.0.1:5433)
cloak: OPENAI_API_KEY, OPENAI_BASE_URL → openai (127.0.0.1:5434)
```

So every integration is the same move: **run your agent (or the tool it
launches) under `cloak run`, and let it read its credential from the injected
environment variable.** If a program already reads `DATABASE_URL`,
`OPENAI_API_KEY`, `OPENAI_BASE_URL`, `ANTHROPIC_API_KEY`, and friends from the
environment — and almost all of them do — it works unchanged.

Register your upstreams first (see the [README](../README.md#commands) for
`cloak add`). The guides below assume they exist.

---

## Claude Code

Wrap the CLI. Everything Claude Code spawns — the Bash tool running `psql`,
an SDK call, a `curl` — inherits the fake environment:

```console
$ cloak run -- claude
```

Inside the session the agent sees only:

```
DATABASE_URL=postgres://cloak:2db1db61ef5ad177@127.0.0.1:5433/pg-prod?sslmode=disable
```

The real password is in your keychain; Cloak swaps it in on the way to the
real database. This single command covers most agent workflows, because
wrapping the top-level process covers every child it creates.

Serve just what a task needs:

```console
$ cloak run --only pg-prod -- claude
```

---

## MCP servers

An MCP server is a process the client launches from a config entry. Wrap that
command with `cloak run` so the server inherits the fake environment.

Given a normal MCP server config:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server-postgres"]
    }
  }
}
```

put `cloak run` in front:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "cloak",
      "args": ["run", "--only", "pg-prod", "--", "npx", "-y", "@some/mcp-server-postgres"]
    }
  }
}
```

The server now reads `DATABASE_URL` (or whatever `--env` you chose) as the
fake loopback DSN, and Cloak proxies to the real database.

**If the server takes the credential as an argument instead of an env var,**
wrap it in a shell so the injected variable expands at launch:

```json
{
  "command": "cloak",
  "args": ["run", "--only", "pg-prod", "--", "sh", "-c", "exec npx -y @some/mcp-server-postgres \"$DATABASE_URL\""]
}
```

The `$DATABASE_URL` is expanded by the shell *inside* the cloak-wrapped
process, so it resolves to the fake DSN — never the real one, which is never
in an environment anywhere.

---

## Cursor (and other MCP-based IDEs)

Cursor launches MCP servers from `~/.cursor/mcp.json` (or a project
`.cursor/mcp.json`). Wrap the server command exactly as in the
[MCP servers](#mcp-servers) section above — this is the clean integration
point:

```json
{
  "mcpServers": {
    "postgres": {
      "command": "cloak",
      "args": ["run", "--only", "pg-prod", "--", "npx", "-y", "@some/mcp-server-postgres"]
    }
  }
}
```

> **Why not wrap the whole IDE?** You *can* launch `cloak run -- cursor`, and
> Cursor's integrated terminal and agents will inherit the fake environment —
> but only if Cursor is not already running (a second launch usually just
> focuses the existing process, which never saw the variables). Wrapping the
> MCP server command is deterministic and does not depend on how the IDE was
> started, so prefer it.

---

## Plain scripts, cron, and CI

Anything that reads its credential from the environment works:

```console
$ cloak run -- python agent.py
$ cloak run -- ./nightly-report.sh
$ cloak run --only stripe -- node ./reconcile.js
```

For non-interactive registration (no TTY to prompt on), pipe the secret in:

```console
$ printf '%s' "$REAL_PASSWORD" | cloak add pg-prod \
    --url postgres://app_user@prod-db.internal:5432/app \
    --env DATABASE_URL --password-stdin
```

This is the one place a real secret touches a pipe rather than a prompt —
keep it out of shell history (a leading space, a secret-manager fetch) and
prefer the interactive prompt when a human is present.

**Headless hosts and CI have no OS keychain.** Set `CLOAK_SECRET_KEY` and
Cloak stores the credential in an encrypted file
(`$XDG_DATA_HOME/cloak/secrets.enc`) instead, with the key derived from that
passphrase. Keep `CLOAK_SECRET_KEY` in your CI secret store — it is the one
value that unlocks the rest.

```console
$ export CLOAK_SECRET_KEY="$CI_CLOAK_PASSPHRASE"
$ printf '%s' "$REAL_PASSWORD" | cloak add pg-prod --url … --env DATABASE_URL --password-stdin
$ cloak run -- ./job.sh
```

Without a keychain *and* without `CLOAK_SECRET_KEY`, `cloak add` fails closed
rather than writing the secret in the clear — that is deliberate.

---

## Agentic backends (LangChain and other LLM libraries)

A backend that calls LLMs programmatically is a prime target: the provider API
key sits in the same process that runs prompt-injectable models, third-party
tools, and observability tracing — exactly where a key should not be. Cloak's
HTTP connector keeps the real key out of it.

It works because LLM SDKs let you override the base URL and read the key from
an environment variable — the same knob used for Azure, gateways, and local
models. Cloak injects a fake key and a loopback base URL; the SDK talks to
Cloak; Cloak swaps in the real key and forwards to the provider over verified
TLS (streaming passes straight through).

Register each provider once:

```console
$ cloak add openai    --type http --host api.openai.com    --auth bearer          --env OPENAI_API_KEY    --env-url OPENAI_BASE_URL
$ cloak add anthropic --type http --host api.anthropic.com --auth header:x-api-key --env ANTHROPIC_API_KEY --env-url ANTHROPIC_BASE_URL
```

Then run the backend under Cloak:

```console
$ cloak run -- python -m myagent.server
cloak: OPENAI_API_KEY, OPENAI_BASE_URL → openai (127.0.0.1:5434)
cloak: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL → anthropic (127.0.0.1:5435)
```

LangChain, like most frameworks, delegates to the provider SDKs, so it
inherits the same knob. The most robust wiring reads Cloak's injected values
explicitly:

```python
import os
from langchain_openai import ChatOpenAI

llm = ChatOpenAI(
    model="gpt-4o",
    base_url=os.environ["OPENAI_BASE_URL"],  # http://127.0.0.1:5434 (from cloak)
    api_key=os.environ["OPENAI_API_KEY"],    # cloak-<token>        (from cloak)
)
```

If you pass nothing, the OpenAI SDK auto-reads `OPENAI_BASE_URL` /
`OPENAI_API_KEY` and it still works. Point `--env-url` at whatever variable
your library expects (some LangChain versions read `OPENAI_API_BASE`) — Cloak
lets you name it.

### Deploying it

Cloak becomes the entrypoint that wraps your server:

```dockerfile
ENTRYPOINT ["cloak", "run", "--"]
CMD ["python", "-m", "myagent.server"]
```

On a headless host the real keys live in Cloak's encrypted store, unlocked by
`CLOAK_SECRET_KEY` (see [above](#plain-scripts-cron-and-ci)). That collapses
your secret-zero to a single passphrase you inject through your orchestrator
(a Kubernetes / ECS secret); the provider keys never enter the agent's
environment, prompts, or traces.

> **Honest note:** the real keys still have to be loaded into the store at
> deploy time (an init step running `cloak add … --password-stdin`), so your
> *pipeline* touches them. What Cloak buys you is that the **running agent** —
> the part that can be prompt-injected or that logs request headers to
> LangSmith / PostHog — only ever holds a fake `cloak-<token>`.

### Limits

- **Don't hardcode the base URL** in code (`base_url="https://api.openai.com"`)
  — that overrides the injected variable. Read it from the environment.
- **AWS Bedrock (SigV4) and GCP Vertex (OAuth) are not supported yet.** Their
  auth signs the request against the real host, so a header swap cannot broker
  them; a native signing connector is planned. Static bearer / api-key
  providers (OpenAI, Anthropic, Groq, OpenRouter, Together, Mistral, most
  gateways) work today.
- **Host compromise still exposes the store and `CLOAK_SECRET_KEY`.** Cloak
  narrows the blast radius to the agent, not the box — see the
  [threat model](THREAT_MODEL.md#the-unprotected-cases-stated-plainly).

---

## Verifying it worked

Two quick checks:

1. **The startup line** printed by `cloak run` on stderr tells you which
   variables were injected and on which loopback port:

   ```
   cloak: DATABASE_URL → pg-prod (127.0.0.1:5433)
   ```

2. **Ask the agent (or the wrapped program) what it sees.** It should report a
   `cloak-<token>` key or a `postgres://cloak:…@127.0.0.1:…` DSN — never your
   real credential. If it can echo the real one, it is not going through
   Cloak; check that the program reads the variable Cloak injected (`--env`)
   and that it was actually launched under `cloak run`.

Confirm the connection itself with the `try it` line `cloak add` prints, e.g.:

```console
$ cloak run -- psql "$DATABASE_URL" -c 'select 1'
```

---

## Gotchas

- **The wrapped program must read its credential from the environment.** If it
  only accepts a hard-coded config file or an argv value, feed it the injected
  variable via `sh -c "… \"$DATABASE_URL\""` as shown above.
- **The token rotates every `cloak run`.** Never persist a fake DSN/key — it
  is dead the moment the session ends. (`cloak import` writes a permanent
  placeholder that intentionally never validates, precisely so a file-resident
  DSN fails closed.)
- **`--tls disable` is for local development only.** It turns off certificate
  verification to the upstream. Never point it at a real service.
- **`--only` is your least-privilege lever.** Serve a single upstream to a
  session that only needs one; see the [threat model](THREAT_MODEL.md#residual-risk).
