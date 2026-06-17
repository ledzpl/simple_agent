# telegram-local-agent

Small Go app that connects Telegram to a local agent such as `codex exec`, Ollama, or a local ChatGPT-style CLI.

It uses Telegram Bot API long polling. You do not need to expose a local HTTP server or open an inbound port.

## Setup

1. Create a Telegram bot with `@BotFather` and copy the bot token.
2. Build the app.

```sh
go build ./...
```

3. Create `.env` from `.env.example`.

```sh
cp .env.example .env
```

4. Check the configuration.

```sh
go run . --check-config
```

5. Start once with only `TELEGRAM_BOT_TOKEN` set, then send `/id` to the bot.

```sh
go run .
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
- `AGENT_BACKEND`: `codex`, `ollama`, or `command`. Default: `codex`.
- `AGENT_TIMEOUT`: local agent timeout. Default: `5m`.
- `AGENT_SYSTEM_PROMPT`: optional instruction prepended to every Telegram message.
- `AGENTS_FILE`: optional JSON file that defines multiple named agents. Default: `agents.json`; missing default file is ignored.
- `DEBATE_ENABLED`: make normal messages run a short multi-agent discussion before the final answer. Default: `false`.
- `MEMORY_ENABLED`: store and reuse per-chat conversation history. Default: `true`.
- `MEMORY_DIR`: directory for chat history JSONL files. Default: `.telegram-memory`.
- `MEMORY_REFINE`: ask the configured backend to distill each successful exchange before storing it. Default: `true`.

Queue limits, progress cadence, state location, debate size, memory window, answer buttons, and dangerous-action confirmation use fixed safe defaults instead of runtime configuration.

- Jobs: one active job per chat, 20 history entries, 60-second progress updates, state in `.telegram-state`.
- Debate: up to four agents, one round, transcript messages enabled.
- Memory: 20 notes, 12,000 context characters, 1,000-character refined notes, 90-second refinement timeout.
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
- Backend-specific fields such as `codex_workdir`, `codex_sandbox`, `ollama_model`, or `command` only when that agent intentionally overrides the global backend behavior.

If an agent file omits `default`, its first agent is used.

Routing:

- Automatic routing picks the highest scoring agent. Longer matching phrases, repeated matches, exact ASCII token matches, and optional `examples` can raise the score.
- If nothing matches, the configured default agent is used.
- Use `/agent <name> <message>` to force a specific agent.
- Use `/agents` in Telegram to list configured agents and match rules.
- Use `/route <message>` to inspect the selected agent, all candidate scores, and match reasons.

## Debate Mode

When `DEBATE_ENABLED=true`, normal user messages are handled as a short discussion:

1. The router selects up to four matched role agents from `agents.json` and includes the default moderator when there is room.
2. Each selected agent writes one concise turn, which is sent to Telegram as it completes.
3. The default agent reads the transcript and writes the final answer.

Use `/agent <name> <message>` for a single-agent answer, bypassing debate. Use `/debate <message>` to force debate even when `DEBATE_ENABLED=false`. Keeping `DEBATE_ENABLED=false` is usually better for Codex because each debate turn starts another backend call.

## Memory

Memory context uses the latest 20 notes up to 12,000 characters. Refined notes are capped at 1,000 characters with a 90-second refinement timeout.

Memory commands:

- `/memory`: show current memory status, storage path, byte count, and invalid JSONL line count.
- `/memory show`: list valid stored memories with 1-based indexes.
- `/memory delete <n>`: delete one stored memory by index.
- `/memory export`: send valid memories as JSONL.
- `/memory repair`: rewrite the memory file with only valid JSONL entries, removing corrupted lines.

Memory loading skips corrupted JSONL lines instead of failing the whole request. New or rewritten memory notes redact common email addresses, Korean and US phone numbers, Telegram bot tokens, and OpenAI-style API keys before storage.

Codex backend:

- `CODEX_BIN`: Codex executable. Default: `codex`.
- `CODEX_WORKDIR`: working directory passed to `codex exec -C`. Default: current directory.
- `CODEX_SANDBOX`: Codex sandbox mode. Default: `read-only`.
- `CODEX_MODEL`: optional Codex model.

Command backend:

- `LOCAL_AGENT_COMMAND`: command that receives the prompt on stdin and writes the answer to stdout.
- `LOCAL_AGENT_COMMAND_ALLOWLIST`: optional comma-separated executable allowlist for `AGENT_BACKEND=command` and per-agent command overrides. Entries match either exact path or basename.

Example:

```sh
AGENT_BACKEND=command
LOCAL_AGENT_COMMAND='chatgpt'
```

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
- Successful user/assistant exchanges are distilled into compact memory notes, appended to a per-chat JSONL file, and included as context on later requests.
- Agent answers include inline buttons for “다시 생성”, “기억 삭제”, and debate transcript viewing.
- Bot responses are sent as plain text.
- Common secrets and personal contact fields are redacted before sending bot messages and before writing memory notes.

## Security Notes

Treat this as remote access to a local agent. Keep these defaults unless you have a reason to loosen them:

- Use `TELEGRAM_ALLOWED_CHAT_IDS`.
- Use `TELEGRAM_ALLOWED_USER_IDS` when the bot is in a chat where more than one Telegram user can send messages.
- Keep `TELEGRAM_ALLOW_GROUPS=false` unless you intentionally run the bot in a group/supergroup.
- Keep `CODEX_SANDBOX=read-only` for Codex.
- Destructive-looking requests always require an explicit `/confirm <id>`.
- Set `LOCAL_AGENT_COMMAND_ALLOWLIST` when using `AGENT_BACKEND=command`.
- The memory directory stores Telegram messages and agent replies in plain JSONL. Keep it out of Git and protect the host account.
- `.telegram-state` stores the Telegram offset and recent job requests in plain JSON. Protect it with the same care as the memory directory.
- Be careful with `AGENT_BACKEND=command`; it intentionally runs the configured local program.

## Operations

Validate runtime configuration before starting:

```sh
telegram-local-agent --check-config
```

Build a container image:

```sh
docker build -t telegram-local-agent .
```

Operational samples are in `deploy/`:

- `deploy/telegram-local-agent.service`: systemd service with restricted filesystem access.
- `deploy/com.example.telegram-local-agent.plist`: launchd plist for macOS.

GitHub Actions in `.github/workflows/ci.yml` runs `go test`, `go vet`, and `go build`. Version tags matching `v*` build Linux and macOS release binaries.
