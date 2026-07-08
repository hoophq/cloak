# Threat model

Cloak makes one narrow, specific claim:

> The real credential never enters the agent's context window, logs, or
> traces — and a fake credential that does leak is worthless off-box and
> expires with the session.

That is the whole promise. Everything below is an honest account of what
that does and does not buy you. If a sentence here reads as hedging, it is
deliberate: a security tool that oversells its guarantees is worse than none.

## What Cloak is

A local proxy. You register an upstream once — the real credential goes into
your OS keychain — and run your agent with `cloak run`. The agent receives a
**fake** DSN or API key pointing at a loopback listener. Cloak validates the
fake token, then reaches the real upstream over verified TLS with the real
credential swapped in.

## The asset

The thing being protected is exactly one thing: **the real credential** — a
database password or an API key. Not the data behind it, not the queries the
agent runs, not the API calls it makes. The credential, and only the
credential.

Why the credential specifically? Because it is the asset with the worst
blast radius when it leaks and the worst hygiene story in an agent workflow.
A leaked query is one query. A leaked long-lived database password or API key
is standing access that nobody rotates out of an LLM transcript, an
observability trace, a shared session link, or a screenshot — the places
secrets go to live forever.

## Trust boundaries

```
┌──────────────────────────────────────────────────────────┐
│ LLM context / transcript / traces      UNTRUSTED (leaky)  │  ← fake token only
├──────────────────────────────────────────────────────────┤
│ Wrapped agent process + anything        SEMI-TRUSTED      │  ← holds a live
│ it spawns (bash, psql, SDK, curl)       (live capability) │     capability, not
│                                                            │     the secret
├──────────────────────────────────────────────────────────┤
│ cloak process + OS keychain             TRUSTED           │  ← real secret
├──────────────────────────────────────────────────────────┤
│ Other local processes as the same user  TRUST BOUNDARY    │  ← assumed benign
├──────────────────────────────────────────────────────────┤
│ Network path to the upstream            UNTRUSTED         │  ← TLS verify-full
└──────────────────────────────────────────────────────────┘
```

Two boundaries matter most:

- **Context is untrusted for confidentiality.** Anything the LLM sees can end
  up somewhere you did not intend. Cloak's entire design is to make sure the
  only credential-shaped thing in that zone is a fake.
- **The wrapped process holds a live capability, not a secret.** While the
  proxy runs, the fake token *works* — it reaches the real upstream. It is a
  capability scoped to this machine and this session, not a reusable secret.
  That distinction is the source of both Cloak's guarantees and its limits.

## Adversaries

| # | Adversary / scenario | Protected? |
|---|---|:--:|
| 1 | Credential leaks into the agent's **context, transcript, or LLM trace** | ✅ Yes |
| 2 | Someone **replays a captured fake credential from another machine** | ✅ Yes |
| 3 | Someone replays a captured fake credential **on this machine after the session ends** | ✅ Yes |
| 4 | Credential leaks into **logs, shell history, or a shared session link** | ✅ Yes |
| 5 | **Network MITM** between Cloak and the upstream | ✅ Yes (TLS `verify-full`) |
| 6 | A **prompt-injected or misbehaving agent misuses the live access** through the proxy | ❌ No |
| 7 | A **hostile local process** running as your user reads Cloak's memory or keychain, or connects to the loopback listener | ❌ No |

### The protected cases, and why

**1, 4 — accidental leakage.** The agent, its tools, its logs, and its traces
only ever see a fake value (`cloak-<token>` or a loopback DSN). There is no
real credential in the confidentiality-untrusted zone to leak. This is the
case Cloak exists for, and the one it handles completely.

**2, 3 — replay.** The fake token is random, minted fresh per `cloak run`
(8 bytes from `crypto/rand`), and only meaningful to a loopback listener that
exists for the life of that one session. Off-box it points at `127.0.0.1` on
the attacker's own machine — nothing. On-box after the session, the listener
is gone and the token no longer validates. Captured credentials fail closed.

**5 — network MITM.** Cloak terminates the agent's loopback connection and
originates a **new** connection to the real upstream with TLS `verify-full`
(full certificate and hostname verification) by default. A network attacker
between Cloak and the upstream cannot present a forged certificate or
downgrade the connection. (`--tls disable` exists for local development only
and turns this off; do not use it against a real upstream.)

### The unprotected cases, stated plainly

**6 — a compromised agent misusing live access.** This is the important one.
While the proxy runs, the fake DSN is a *working* connection to your real
database, and the fake key is *working* access to your real API. A
prompt-injected agent that decides to `DROP TABLE` or exfiltrate rows can do
so — Cloak will faithfully proxy those requests, because it cannot tell a
malicious query from a legitimate one. **Cloak protects the credential, not
the access.** The credential half and the access-control half are different
problems; Cloak solves the first and deliberately does not pretend to solve
the second (see [Residual risk](#residual-risk)).

**7 — a hostile local process.** Cloak assumes the machine is not already
owned. A process running as your user can connect to the loopback listener,
read Cloak's process memory, or read the OS keychain the same way any of your
own tools can. If an attacker already has code execution as you, the
credential was never yours to protect in the first place. Cloak is not a
sandbox and does not raise a privilege boundary against your own account.

## Residual risk

The single residual risk worth internalizing:

> **The fake DSN/key is a live capability for as long as the proxy runs.**

Cloak shrinks the *lifetime* and *portability* of a leak to near zero, but it
does not shrink the *authority* the agent wields during the session. Manage
that authority with the layers Cloak is designed to sit alongside:

- **Scope the upstream credential itself.** Cloak proxies whatever the
  credential can do. A read-only database role or a least-privilege API key
  bounds the blast radius of case #6 far more than anything Cloak can.
- **Run only what you need.** `cloak run --only pg-prod -- …` serves a single
  upstream, so an agent that only needs the database is never handed live API
  access.
- **Keep sessions short.** The capability exists exactly as long as
  `cloak run` runs. When the wrapped command exits, every listener closes and
  every token dies.
- **Pair with the agent's own sandboxing and permission controls.** Tool
  allow-lists, filesystem sandboxes, and human-in-the-loop approval are what
  address the misuse half. Cloak addresses the leakage half. You want both.

## Non-goals

Things Cloak deliberately is **not**, so no one has to discover it the hard
way:

- **Not a sandbox.** It does not confine the agent or restrict what it can do
  with the access it is granted.
- **Not an authorization layer.** It does not inspect, filter, or approve
  queries and requests. Every proxied request runs with the full authority of
  the real credential.
- **Not an egress firewall.** It does not stop the agent from reaching the
  network by other means.
- **Not a secrets manager.** It stores one credential per upstream in the OS
  keychain for its own use; it is not a vault, has no sharing or rotation
  workflow, and is not where your team's secrets should live.

## Design choices that back the claims

For readers who want to verify the guarantees rather than take them on faith:

- **Real credentials live in the OS keychain**, referenced by upstream name.
  The on-disk config contains no secrets, and credentials are never passed on
  the command line (`cloak add` rejects passwords in `--url` and prompts on
  the TTY instead).
- **On hosts with no keychain** (headless Linux, CI), setting
  `CLOAK_SECRET_KEY` switches storage to an encrypted file: AES-256-GCM with a
  key derived from that passphrase (PBKDF2-HMAC-SHA256, 600k iterations), the
  entry name bound as the AEAD's additional data. The passphrase is never
  written to disk; a stolen `secrets.enc` is opaque without it. Without a
  keychain *and* without `CLOAK_SECRET_KEY`, Cloak fails closed rather than
  writing a secret in the clear.
- **Per-session tokens** are random and minted fresh on every `cloak run`.
- **Loopback-only listeners.** Every listener binds `127.0.0.1`, never a
  routable interface.
- **Constant-time token comparison** (`crypto/subtle`) on both the Postgres
  and HTTP paths, so a bad token cannot be recovered by timing.
- **Upstream errors are reduced before relay.** Postgres upstream errors are
  cut down to their SQLSTATE code before being returned to the agent, so a
  real username or server detail cannot leak back through an error message.
- **TLS is re-originated with `verify-full`** to the real upstream regardless
  of what the loopback leg looks like.

### Known limitations of the design

- **No SCRAM channel binding.** Because Cloak terminates the agent's
  connection and re-originates its own, it negotiates SCRAM-SHA-256 (not the
  `-PLUS` channel-binding variant) with the upstream. TLS `verify-full` is the
  defense against a MITM on that leg; channel binding would be additional
  defense-in-depth that this architecture cannot offer.
- **Cloak sees the traffic.** By construction it is on the path: it splices
  Postgres bytes and proxies HTTP requests. It does not log or persist request
  bodies, but it is not an end-to-end-encrypted tunnel and is not designed to
  be blind to the data. Trust in the Cloak process is assumed (it is in the
  trusted zone above).
- **The encrypted-file backend is only as strong as `CLOAK_SECRET_KEY`.**
  When it is used, the passphrase becomes the asset — protect it as you would
  the credential itself (a CI secret store is the right home). The OS keychain
  is the stronger default and is used whenever one is available.

## In one sentence

Cloak guarantees your real credential stays out of the places it would leak
and rot; it does not guarantee the agent behaves once handed a working
connection. Use it for the first problem, and layer sandboxing and least
privilege for the second.
