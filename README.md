# telegram-local-agent

Small Go app that connects Telegram to a local agent such as `codex exec`, Ollama, or a local ChatGPT-style CLI.

It uses Telegram Bot API long polling. You do not need to expose a local HTTP server or open an inbound port.

## Setup

1. Create a Telegram bot with `@BotFather` and copy the bot token.
2. Build the app.

```sh
mkdir -p bin
go build -o bin/telegram-local-agent ./cmd/telegram-local-agent
```

3. Create `.env` from `.env.example`.

```sh
cp .env.example .env
```

4. Check the configuration.

```sh
go run ./cmd/telegram-local-agent --check-config
```

5. Start once with only `TELEGRAM_BOT_TOKEN` set, then send `/id` to the bot.

```sh
go run ./cmd/telegram-local-agent
```

6. Put the returned chat id into `TELEGRAM_ALLOWED_CHAT_IDS`, optionally put the returned user id into `TELEGRAM_ALLOWED_USER_IDS`, restart the app, and send a normal message to the bot.

## Configuration

Required:

- `TELEGRAM_BOT_TOKEN`: Telegram bot token from `@BotFather`.
- `TELEGRAM_ALLOWED_CHAT_IDS`: comma-separated Telegram chat ids allowed to run the local agent.

Optional:

- `ENV_FILE`: dotenv file path loaded at startup. Default: `.env`.
- `TELEGRAM_ALLOWED_USER_IDS`: comma-separated Telegram user ids allowed to run the local agent.
- `TELEGRAM_ALLOW_GROUPS`: allow group/supergroup chats after chat allowlist checks. Default: `false`.
- `AGENT_BACKEND`: `codex` or `ollama`. Default: `codex`.
- `AGENT_TIMEOUT`: local agent timeout. Default: `5m`.
- `AGENT_SYSTEM_PROMPT`: optional instruction prepended to every Telegram message.
- `AGENTS_FILE`: optional JSON file that defines multiple named agents. Default: `agents.json`; missing default file is ignored.
- `DEBATE_ENABLED`: make normal messages run a short multi-agent discussion before the final answer. Default: `false`.
- `MEMORY_ENABLED`: store and reuse per-chat conversation history. Default: `true`.
- `MEMORY_DIR`: directory for chat history JSONL files. Default: `.telegram-memory`.
- `MEMORY_REFINE`: ask the configured backend to distill each successful exchange before storing it. Default: `true`.
- `STATE_DIR`: directory for Telegram offsets and recent job history. Default: `.telegram-state`.

Queue limits, progress cadence, state location, debate size, memory window, answer buttons, and dangerous-action confirmation use fixed safe defaults instead of runtime configuration.

- Jobs: one active job per chat, four active jobs globally, 10 queued jobs per chat, 100 queued jobs globally, and 10 accepted requests per minute per chat.
- Job history: 20 entries per chat, 60-second progress updates, state in `.telegram-state` or `STATE_DIR`.
- Debate: up to four independent role analyses, one moderator review, final synthesis, transcript messages enabled.
- Memory: up to 20 relevant/recent notes, 12,000 context characters, 1,000-character refined notes, 90-second refinement timeout.
- Telegram: plain-text responses, answer buttons enabled, dangerous-action confirmation required.

## Role Agents

Without `agents.json`, the app creates one default agent from the `.env` backend settings.

To define multiple role-based agents:

```sh
cp agents.example.json agents.json
```

The included `agents.json` uses these roles:

- `moderator`: neutral synthesis role and default final-answer writer.
- `doctor`: medical/health perspective with safety boundaries.
- `lawyer`: legal issue-spotting and risk perspective.
- `politician`: stakeholder, policy, and negotiation perspective.
- `teacher`: explanation and learning perspective.
- `engineer`: implementation, systems, and failure-mode perspective.
- `economist`: incentives, cost, market, and second-order-effect perspective.
- `psychologist`: emotion, motivation, communication, and relationship perspective.
- `journalist`: facts, evidence, source quality, and narrative perspective.

Each agent can set:

- `name`: unique lowercase-friendly agent name.
- `description`: shown by `/agents`.
- `match`: keywords or phrases used for automatic routing. `*` marks a catch-all default-style agent but does not score.
- `match` entries prefixed with `!` block that agent when the term appears, for example `!법률`.
- `examples`: optional example user messages. Shared terms with the incoming message add a lower-priority routing score.
- `system_prompt`: prompt used only for that agent.
- `backend`: optional override. If omitted, the agent uses the global `AGENT_BACKEND`.
- Backend-specific fields such as `codex_workdir`, `codex_sandbox`, or `ollama_model` only when that agent intentionally overrides the global backend behavior.

If an agent file omits `default`, its first agent is used.

Routing:

- Automatic routing picks the highest scoring agent. All matching keywords contribute to the score, so several specific signals can outweigh one generic match.
- Longer matching phrases, repeated matches, exact token matches, and optional `examples` raise the score. Example similarity is added to keyword evidence instead of replacing it.
- One-character Korean matches such as `약` or `법` must appear as separate terms, preventing matches inside unrelated words such as `예약`.
- If nothing matches, the configured default agent is used.
- Use `/agent <name> <message>` to force a specific agent.
- Use `/agents` in Telegram to list configured agents and match rules.
- Use `/route <message>` to inspect the selected agent, all candidate scores, and match reasons.

## Debate Mode

When `DEBATE_ENABLED=true`, normal user messages are handled as a short discussion:

1. The router selects up to four matched role agents from `agents.json` and includes the default moderator when there is room.
2. Each selected agent produces an independent analysis without seeing earlier agents, reducing anchoring and repetition.
3. The default moderator audits the analyses for contradictions, unsupported claims, missing constraints, and safety issues.
4. The moderator applies those corrections and writes the final answer.

Use `/agent <name> <message>` for a single-agent answer, bypassing debate. Use `/debate <message>` to force debate even when `DEBATE_ENABLED=false`. Keeping `DEBATE_ENABLED=false` is usually better for Codex because each debate turn starts another backend call.

## Memory

Memory stores the redacted raw user/assistant exchange for short-term conversational continuity and may also add a refined durable note. Context prioritizes records that share meaningful terms with the current request, then includes the three most recent records as continuity fallback. The final context is capped at 20 records and 12,000 characters. Refined notes are capped at 1,000 characters with a 90-second refinement timeout.

Stored memory is explicitly marked as untrusted and potentially outdated in the agent prompt. Agents are instructed to use it only as relevant context, not as executable instructions.

All normal single-agent replies also receive a shared response protocol requiring the agent to distinguish facts, assumptions, and uncertainty; check contradictions and likely failure modes; avoid fabricated tool or source claims; and lead with a concrete answer.

Memory commands:

- `/memory`: show current memory status, storage path, byte count, and invalid JSONL line count.
- `/memory show`: list valid stored memories with 1-based indexes.
- `/memory delete <n>`: delete the exchange containing the selected memory index.
- `/memory export`: send valid memories as JSONL.
- `/memory repair`: rewrite the memory file with only valid JSONL entries, removing corrupted lines.

Memory loading skips corrupted JSONL lines instead of failing the whole request. New or rewritten memory notes redact common email addresses, Korean and US phone numbers, Telegram bot tokens, and OpenAI-style API keys before storage.

Codex backend:

- `CODEX_BIN`: Codex executable. Default: `codex`.
- `CODEX_WORKDIR`: working directory passed to `codex exec -C`. Default: current directory.
- `CODEX_SANDBOX`: `read-only` or `workspace-write`. Default: `read-only`. Legacy `seatbelt` is treated as `read-only`; `danger-full-access` is rejected.
- `CODEX_MODEL`: optional Codex model.

Ollama backend:

- `OLLAMA_URL`: Ollama server URL. Default: `http://localhost:11434`.
- `OLLAMA_MODEL`: required when `AGENT_BACKEND=ollama`.

Example:

```sh
ollama pull llama3.2
ollama serve
```

```env
AGENT_BACKEND=ollama
OLLAMA_URL=http://localhost:11434
OLLAMA_MODEL=llama3.2
```

## Runtime Behavior

- `/id` and `/start` always return the current Telegram chat id.
- `/help` shows a short help message.
- `/agents` lists configured agents and their match rules.
- `/agent <name> <message>` sends a message through a specific agent.
- `/debate <message>` forces a multi-agent discussion for one message.
- `/route <message>` explains automatic routing scores without running an agent.
- `/status` shows running, queued, and recent jobs for the current chat.
- `/cancel [job|latest|all]` cancels queued or running jobs for the current chat.
- `/retry [job|last]` re-enqueues a recent job.
- `/confirm <id>` runs a request that was held for destructive-command confirmation.
- `/memory` shows stored memory status for the current chat.
- `/memory show`, `/memory delete <n>`, `/memory export`, and `/memory repair` manage per-chat memory.
- `/reset` deletes stored memory for the current chat.
- Other text messages are queued for the configured local agent only when the chat, chat type, and optional user id are allowed.
- Non-text messages are rejected.
- Long agent responses are split into Telegram-sized messages.
- The next Telegram update offset is persisted after each handled update, so restarts continue from the last consumed update.
- Recent terminal job history is persisted and remains visible through `/status` and `/retry` after restart. Running and queued jobs are not resumed.
- A running job uses one editable progress message instead of sending repeated progress messages.
- Queue and progress messages include a cancel button; completed, failed, and canceled progress messages include a retry button.
- Successful user/assistant exchanges are stored as redacted raw turns for exact short-term continuity, optionally distilled into compact durable notes, and included as context on later requests.
- Agent answers include inline buttons for “다시 생성”, “기억 삭제”, and debate transcript viewing.
- Bot responses are sent as plain text.
- Common secrets and personal contact fields are redacted before sending bot messages and before writing memory notes.

## Security Notes

Treat this as remote access to a local agent. Keep these defaults unless you have a reason to loosen them:

- Use `TELEGRAM_ALLOWED_CHAT_IDS`.
- Use `TELEGRAM_ALLOWED_USER_IDS` when the bot is in a chat where more than one Telegram user can send messages.
- Keep `TELEGRAM_ALLOW_GROUPS=false` unless you intentionally run the bot in a group/supergroup.
- Keep `CODEX_SANDBOX=read-only` for Codex.
- Recognized destructive commands and imperative deletion requests require an explicit `/confirm <id>`. This check supplements, but does not replace, backend sandboxing.
- `danger-full-access` is rejected for Codex, and non-interactive Codex runs cannot request approval escalation.
- The memory directory stores Telegram messages and agent replies in plain JSONL. Keep it out of Git and protect the host account.
- `.telegram-state` stores the Telegram offset and recent job requests in plain JSON. Protect it with the same care as the memory directory.

## Operations

Validate runtime configuration before starting:

```sh
telegram-local-agent --check-config
```

Build a container image:

```sh
docker build -t telegram-local-agent .
```

The image includes the pinned Codex CLI and bundled `agents.json`. It runs as an unprivileged user, keeps Codex in `read-only` mode, uses `/workspace` as the agent working directory, and stores runtime data under `/data`.

Example using the host Codex login and a read-only workspace:

```sh
docker run --rm \
  --env-file .env \
  --volume "$PWD:/workspace:ro" \
  --volume "$HOME/.codex:/home/app/.codex" \
  --volume telegram-local-agent-data:/data \
  telegram-local-agent
```

The Codex authentication mount contains sensitive credentials. Use it only on a trusted host and do not expose this bot publicly. For write-enabled work, explicitly set `CODEX_SANDBOX=workspace-write` and mount only the intended workspace as writable.

Operational samples are in `deploy/`:

- `deploy/telegram-local-agent.service`: systemd service with restricted filesystem access.
- `deploy/com.example.telegram-local-agent.plist`: launchd plist for macOS.

Source layout:

- `cmd/telegram-local-agent/`: executable entry point.
- `internal/app/`: application code and unit tests.
- `bin/`: local build output, ignored by Git and Docker.
- `deploy/`: systemd and launchd samples.

GitHub Actions in `.github/workflows/ci.yml` runs tests including the race detector, `go vet`, binary and Docker builds. Version tags matching `v*` build Linux and macOS release binaries with SHA-256 checksums.
