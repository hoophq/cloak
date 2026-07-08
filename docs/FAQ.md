# FAQ

### Why not just use environment variables?

Because the agent can read them. `DATABASE_URL` in the environment is
`DATABASE_URL` the agent can `echo`, print in a stack trace, or hand to the
LLM verbatim. The problem was never *where* the credential sits — it is that
the credential is *real* wherever the agent can reach it. Cloak puts a **fake**
value in that same environment variable, so the agent reading it, logging it,
or leaking it costs you nothing.

### Why not a secrets-manager CLI (Vault, 1Password, `aws secretsmanager`)?

Those solve a different problem: getting the real secret *to* the workload
safely. That is genuinely useful — but the value they hand you is the real
secret, and the moment it lands somewhere the agent can read (an env var, a
file, a variable), you are back to the original problem. `vault read` →
`DATABASE_URL=<real password>` is exactly the leak Cloak exists to prevent.
Cloak is complementary: keep fetching credentials with your secrets manager if
you like, then register them with Cloak so the agent only ever sees a fake.

### How is this different from Infisical Agent Vault?

Agent Vault is vault-centric and, today, HTTP/API-key oriented — it brokers
API credentials to agents from a hosted secrets platform. Cloak is a local,
zero-infrastructure single binary with **databases as a first-class citizen**:
it speaks the Postgres wire protocol directly (SCRAM-SHA-256, transparent byte
splice) as well as HTTP. If your agents touch production databases from a
developer laptop and you do not want to stand up a platform to do it, that is
the gap Cloak fills.

### How is this different from CyberArk Secretless Broker?

Secretless Broker is the same core idea — an app connects to a local broker
that injects the real credential — proven at scale, but built for
**Kubernetes and service infrastructure**: you deploy it as a sidecar and
manage it as part of your platform. Cloak takes that idea to the **developer
laptop and the agent workflow**: one binary, `cloak run -- <agent>`, no
cluster, no sidecar, credentials in your OS keychain. Same principle, very
different ergonomics and target.

### Does the LLM provider still see my real API key when I proxy it through Cloak?

Yes — and it has to. If you register `api.openai.com` as an upstream, Cloak is
the thing that calls OpenAI, and it authenticates with your real key over TLS.
OpenAI receives the real key because that is how it knows the request is
yours. What Cloak prevents is your key leaking into *the agent's* context,
logs, or traces — a different exposure surface from the provider you are
deliberately authenticating to.

### Does Cloak see my data, queries, and requests?

By construction, yes — it is on the path. Cloak splices Postgres bytes and
proxies HTTP requests, so the traffic passes through its process. It does not
log or persist request bodies, and it is a local process you run yourself, but
it is not an end-to-end-encrypted tunnel and is not designed to be blind to
the data. The [threat model](THREAT_MODEL.md#trust-boundaries) places the
Cloak process in the trusted zone for exactly this reason.

### Is the fake DSN a security boundary?

No. It is a **live capability**, not a secret and not a sandbox. While the
proxy runs, the fake DSN is a working connection to your real database and the
fake key is working access to your real API. Cloak guarantees the *credential*
does not leak and does not outlive the session — it does not stop an agent
from misusing the access while it holds it. Bound that with a least-privilege
upstream credential and your agent's own sandboxing. This is the single most
important thing to understand about Cloak; the
[threat model](THREAT_MODEL.md#the-unprotected-cases-stated-plainly) covers it
in full.

### Can a compromised or prompt-injected agent still do damage?

Yes, within the authority of the credential you registered. Cloak faithfully
proxies whatever the agent sends; it cannot distinguish a malicious query from
a legitimate one. That is why the credential you give Cloak should be
least-privilege (a read-only role goes a long way), why `cloak run --only`
exists, and why Cloak is meant to sit *alongside* agent sandboxing, not
replace it.

### What happens to the fake credential after the session ends?

It dies. Every listener closes and every token stops validating the moment the
wrapped command exits. A fake DSN captured from a transcript is inert
afterward — and it was always inert from any other machine, since it points at
`127.0.0.1`.

### Which platforms are supported?

macOS and Linux today, as a single static binary. Credentials are stored via
the OS keychain (macOS Keychain, Linux Secret Service). Postgres and HTTP(S)
upstreams are supported now; more protocols are planned.

### How does this work on a headless server or in CI, where there's no keychain?

Set `CLOAK_SECRET_KEY`. Its presence switches Cloak from the OS keychain to an
encrypted-file backend (`$XDG_DATA_HOME/cloak/secrets.enc`): AES-256-GCM with
the key derived from that passphrase via PBKDF2. Store `CLOAK_SECRET_KEY` in
your CI secret store — it becomes the single value that unlocks the rest, so
the file at rest is useless without it. If no keychain is available and
`CLOAK_SECRET_KEY` is unset, `cloak add` fails closed instead of silently
writing the secret in the clear. See the
[threat model](THREAT_MODEL.md#design-choices-that-back-the-claims).

### Do I have to change my application or agent code?

No, as long as it reads its credential from an environment variable — which
the standard clients and SDKs do (`DATABASE_URL`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL`, `ANTHROPIC_API_KEY`, …). You register the upstream once and
launch under `cloak run`. See the [integration guides](INTEGRATIONS.md).
