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

4. Start once with only `TELEGRAM_BOT_TOKEN` set, then send `/id` to the bot.

```sh
go run .
```

5. Put the returned chat id into `TELEGRAM_ALLOWED_CHAT_IDS`, restart the app, and send a normal message to the bot.

## Configuration

Required:

- `TELEGRAM_BOT_TOKEN`: Telegram bot token from `@BotFather`.
- `TELEGRAM_ALLOWED_CHAT_IDS`: comma-separated Telegram chat ids allowed to run the local agent.

General:

- `AGENT_BACKEND`: `codex`, `ollama`, or `command`. Default: `codex`.
- `AGENT_TIMEOUT`: local agent timeout. Default: `5m`.
- `AGENT_SYSTEM_PROMPT`: optional instruction prepended to every Telegram message.
- `AGENTS_FILE`: optional JSON file that defines multiple named agents. Default: `agents.json`; missing default file is ignored.
- `DEFAULT_AGENT`: fallback agent name when no route matches if the agent file does not set `default`. Built-in `agents.json` uses `moderator`.
- `DEBATE_ENABLED`: make normal messages run a short multi-agent discussion before the final answer. Default: `false`.
- `DEBATE_MAX_AGENTS`: max agents invited to one discussion. Default: `4`.
- `DEBATE_ROUNDS`: discussion rounds before synthesis. Default: `1`.
- `DEBATE_SHOW_TRANSCRIPT`: send each agent turn to Telegram. Default: `true`.
- `TELEGRAM_ALLOW_ALL`: development-only escape hatch. Default: `false`.
- `TELEGRAM_PARSE_MODE`: optional Telegram parse mode for outgoing messages: `Markdown`, `MarkdownV2`, or `HTML`. Default: plain text.
- `TELEGRAM_ANSWER_ACTIONS`: attach inline buttons to agent answers for regenerate, recent memory deletion, and debate transcript viewing. Default: `true`.

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

Routing:

- Automatic routing picks the highest scoring agent. Longer matching phrases, repeated matches, exact ASCII token matches, and optional `examples` can raise the score.
- If nothing matches, the configured default agent is used.
- Use `/agent <name> <message>` to force a specific agent.
- Use `/agents` in Telegram to list configured agents and match rules.
- Use `/route <message>` to inspect the selected agent, all candidate scores, and match reasons.

## Debate Mode

When `DEBATE_ENABLED=true`, normal user messages are handled as a short discussion:

1. The router selects matched role agents from `agents.json`, capped by `DEBATE_MAX_AGENTS`, and includes the default moderator when there is room.
2. Each selected agent writes a concise turn. If `DEBATE_SHOW_TRANSCRIPT=true`, those turns are sent to Telegram as they happen.
3. The default agent reads the transcript and writes the final answer.

Use `/agent <name> <message>` for a single-agent answer, bypassing debate. Use `/debate <message>` to force debate even when `DEBATE_ENABLED=false`. Keeping `DEBATE_ENABLED=false` is usually better for Codex because each debate turn starts another backend call.

## Memory

- `MEMORY_ENABLED`: store and reuse per-chat conversation history. Default: `true`.
- `MEMORY_DIR`: directory for chat history JSONL files. Default: `.telegram-memory`.
- `MEMORY_MAX_MESSAGES`: max recent messages inserted into each agent prompt. Default: `20`.
- `MEMORY_MAX_CHARS`: max recent message characters inserted into each agent prompt. Default: `12000`.
- `MEMORY_REFINE`: ask the configured backend to distill each successful exchange before storing it. Default: `true`.
- `MEMORY_REFINE_MAX_CHARS`: max characters for one stored memory note. Default: `1000`.
- `MEMORY_REFINE_TIMEOUT`: timeout for the memory distillation call. Default: `90s`.

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
- `CODEX_EXTRA_ARGS`: optional extra args appended before the prompt, for example `--search`.

Command backend:

- `LOCAL_AGENT_COMMAND`: command that receives the prompt on stdin and writes the answer to stdout.

Example:

```sh
AGENT_BACKEND=command
LOCAL_AGENT_COMMAND='chatgpt'
```

Ollama backend:

- `OLLAMA_URL`: Ollama server URL. Default: `http://localhost:11434`.
- `OLLAMA_MODEL`: required when `AGENT_BACKEND=ollama`.
- `OLLAMA_KEEP_ALIVE`: optional Ollama model keep-alive value, for example `5m`.

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
- `/memory` shows stored memory status for the current chat.
- `/memory show`, `/memory delete <n>`, `/memory export`, and `/memory repair` manage per-chat memory.
- `/reset` deletes stored memory for the current chat.
- Other text messages are forwarded to the configured local agent only when the chat id is allowed.
- Non-text messages are rejected.
- Long agent responses are split into Telegram-sized messages.
- Successful user/assistant exchanges are distilled into compact memory notes, appended to a per-chat JSONL file, and included as context on later requests.
- Agent answers can include inline buttons for “다시 생성”, “기억 삭제”, and debate transcript viewing when `TELEGRAM_ANSWER_ACTIONS=true`.
- `TELEGRAM_PARSE_MODE` is retried as plain text if Telegram rejects the formatted message.

## Security Notes

Treat this as remote access to a local agent. Keep these defaults unless you have a reason to loosen them:

- Use `TELEGRAM_ALLOWED_CHAT_IDS`.
- Keep `CODEX_SANDBOX=read-only` for Codex.
- Avoid `TELEGRAM_ALLOW_ALL=true` outside a throwaway test.
- The memory directory stores Telegram messages and agent replies in plain JSONL. Keep it out of Git and protect the host account.
- Be careful with `AGENT_BACKEND=command`; it intentionally runs the configured local program.
