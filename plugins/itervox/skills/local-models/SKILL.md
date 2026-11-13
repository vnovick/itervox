---
name: local-models
description: 'Use when the user wants an Itervox profile to use a model server on their own machine: "run itervox agents on a local model", "use colibri with itervox", "point claude at 127.0.0.1", "fall back to a local model when Anthropic is rate limited", "doctor says my model server is not answering". Sets the base-URL variable at the start of the profile command, raises the agent timeouts, gives the local profile its own pool, adds it to agent.backend_fallback, and checks it with the doctor Model servers section.'
---

# Run a profile on a local model server

Sources: `guides/local-models.mdx`, `configuration.mdx` (backend health and
fallback, `agent.backend_fallback`), `guides/backend-health.mdx`,
`guides/backends.mdx` (OpenCode), `cli.mdx` (`itervox doctor`).

Set expectations first: a large local model decodes at a few tokens per
second on ordinary hardware, and an implementer run can use hundreds of
thousands of tokens. Start with small jobs (dependency analysis, triage,
summaries, small reviews) and measure. A local model does not make a run
safer: the agent CLI has the same permissions whichever model it talks to.

## 1. Start the server

colibri (https://github.com/JustVugg/colibri) serves an OpenAI-compatible
`/v1` and an Anthropic-compatible `/v1/messages`:

```sh
coli serve        # 127.0.0.1:8000
```

The user runs it in their own terminal; any server with those APIs works the
same way.

## 2. Point a profile at it

Profiles are in `WORKFLOW.md` under `agent.profiles`. Put the base-URL
assignment at the start of the command. Itervox skips leading `NAME=value`
assignments when it works out the backend, so this is still the `claude`
backend, with the same flags, resume and parsing:

```yaml
agent:
  profiles:
    deps-analyzer-local:
      command: ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude --model <model the server serves>
      soul_file: .itervox/agents/deps-analyzer/SOUL.md
      instructions_file: .itervox/agents/deps-analyzer/INSTRUCTIONS.md
```

- **Claude Code** reads `ANTHROPIC_BASE_URL`, without `/v1`. If the server
  needs a key, the user stores it with `itervox secret set ANTHROPIC_AUTH_TOKEN`
  (never in the command).
- **OpenCode**: define an OpenAI-compatible provider in `opencode.json` (base
  URL `http://127.0.0.1:8000/v1`) and pick it with `--model <provider>/<model>`.
- **Codex** reads custom providers from `~/.codex/config.toml`, not from the
  command.
- Do not combine a local profile with `agent.ssh_hosts`: a run on a worker host
  looks for the server on that host.

## 3. Give slow runs time

| Setting | Default | Set it to |
|---|---|---|
| `agent.turn_timeout_ms` | `3600000` (1 h) | the longest a whole run may take |
| `agent.stall_timeout_ms` | `300000` (5 min) | more than the slowest gap between two events |
| `agent.read_timeout_ms` | `30000` | more than the slowest single response chunk |

```bash
itervox config set agent.turn_timeout_ms 14400000
itervox config set agent.stall_timeout_ms 1800000
itervox config set agent.read_timeout_ms 600000
```

These apply to every profile; a running daemon reloads on the change.

## 4. Check it

Run `itervox doctor` and read **Model servers**: every server a profile's
command points at, the profiles that use it, and a 3-second probe (any HTTP
answer counts):

```text
Model servers
  ANTHROPIC_BASE_URL=http://127.0.0.1:8000, used by deps-analyzer-local: not answering
WARNING: deps-analyzer-local point their agent at http://127.0.0.1:8000, which did not answer (…); …
```

The warning does not change the exit code. Fix it by starting the server or
correcting the URL. A URL from a variable (`$LOCAL_MODEL_URL`) is listed but
not checked, and doctor sees only assignments in the command itself, not a
CLI's own config file.

## 5. Fall back to it when a vendor limit is hit

Itervox stops dispatching to a backend whose usage limit was hit and can
reroute through `agent.backend_fallback`. Give the local profile its own
`pool`, so a limit on Anthropic does not hold it, and add `<backend>/<pool>` as
a step in the chain. The profiles go in `WORKFLOW.md`; `backend_fallback` goes
under `agent` in `.itervox/settings.jsonc` (or the front matter):

```yaml
agent:
  profiles:
    coder:
      command: claude
    coder-local:
      command: ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude --model <model>
      pool: local
  backend_fallback:
    chain: [claude, codex, claude/local]
    profile_map:
      coder: { "claude/local": coder-local }
    switch_back: at_reset      # or on_success, manual
```

When `claude` is limited and nothing earlier in the chain is healthy, `coder`
issues run on `coder-local`, and switch back per `switch_back`. A running
daemon reloads when `WORKFLOW.md` or a settings file changes.

## Done means

`itervox doctor`'s Model servers section shows the server answering, and
`itervox config show` lists the local profile under **How agents run**.
