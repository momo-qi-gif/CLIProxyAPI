# CliRelay compatibility port

This branch ports missing Claude-to-Codex compatibility behavior from the user's deployed CliRelay build onto CLIProxyAPI v8. It does not import CliRelay's management UI, identity system, PostgreSQL/Redis runtime, or operational configuration.

## Source and target

- Target repository: `git@github.com:momo-qi-gif/CLIProxyAPI.git`.
- Target baseline: `01b24f1f95dc9b309765212c52b25b25e005ff01`.
- Branch: `fix/clirelay-claude-compat`.
- Source build: `v0.5.9-claude-stream-repair`, based on CliRelay commit `11d8df955fafc72303c6e1b29fe1e648908fe6de`.
- Source binary SHA256: `27d8d0fe010e034e2234ed62d596fb31c9ba1f454ee761acf5fb2456f816e5ed`.
- Source combined patch SHA256 from the reviewed release: `9129813fd86eb1d00078c347f9724ca45d92d4993cb0431692cc9d3972ccc2b2`.

The source binary bind mount and hash were checked read-only on vps20005 during this port. No production configuration, accounts, credentials, containers, or binaries were changed.

## Ported behavior

### Visible text and refusals

The Codex-to-Claude translator now accepts visible text and refusal content from deltas, text/refusal done events, content-part snapshots, completed message items, and terminal output snapshots. A snapshot appends only the suffix not already emitted for the same output/content part. Deferred events are replayed before terminal text reconciliation. Repeated terminal events and late events after `message_stop` are ignored.

Refusal text is also retained in non-streaming Claude responses. Upstream commentary-to-thinking policy, commentary signatures, native web search, and reasoning usage extraction remain in place.

### Tool calls

Tool arguments remain independent across interleaved calls, with explicit identity conflicts rejected rather than appended to another call. Other content keeps the upstream deferred-event FIFO.

A tool block is released only after the call completes and its arguments form a valid JSON object. This deliberately changes upstream's early `tool_use` delivery: even the first tool block can start later. Malformed or truncated arguments are not exposed as executable streaming tool calls. Non-streaming length-truncated calls with invalid arguments are omitted. A length-limited response retains `stop_reason: max_tokens`, including when a valid tool call was emitted earlier.

Forced tool selection with a missing or undeclared function name falls back to `auto`. No actual tools means no `tool_choice`. Existing additional-tools declarations, native web-search selection, shortened names, and parallel-tool control are retained.

### Thinking effort

Recognized explicit `low`, `medium`, `high`, `xhigh`, or `max` effort overrides an enabled numeric thinking budget in both request translation and the canonical thinking parser. Disabled thinking and explicit model suffixes retain priority. Target-model validation still clamps unsupported levels. Administrator payload rules remain the final semantic barrier; the port does not add a post-finalizer rewrite.

The source-aware canonical fallback also handles Claude requests without resolved model information so a translator default does not mask explicit source intent. Other unresolved source formats are not broadened by that fallback.

Adaptive and standalone effort values retain upstream's normalization and passthrough behavior, including `minimal`. For `thinking.type: enabled` with an explicit numeric budget, only the five recognized override levels above take priority; `minimal` retains the upstream budget-first behavior for that combination.

### Terminal failures

For a Claude source, only a `max_output_tokens` incomplete terminal is accepted as a normal partial completion. Other incomplete reasons produce an explicit upstream error. Known-safe reason labels are used in errors instead of reflecting arbitrary upstream strings.

The source-aware check is applied across HTTP Execute/ExecuteStream and WebSocket execution paths. OpenAI Responses incomplete behavior is retained. Existing failed/error and missing-terminal handling is preserved.

## Already covered by upstream or intentionally retained

- Top-level string system messages, HTTP(S) URL images, base64 images, and disabling parallel tool use already have upstream implementations.
- Mid-conversation system messages retain upstream's reminder/additional-tools representation and tool-result adjacency handling. This is **not** a literal port of CliRelay's unconditional `system` to `developer` role replacement.
- CliRelay's PostgreSQL `thinking_level` log patch is not copied into this different logging architecture. CLIProxyAPI already records post-translation effort through `SetTranslatedRequestMetadata` / `SetTranslatedReasoningEffort`. That value has different semantics from CliRelay's client-request effort field.
- No new database or Redis dependency is introduced.

## Verification

The port uses table-driven translator regressions, real HTTP and WebSocket executor tests, canonical thinking tests, and synthetic upstream fixtures. Test credentials are local placeholders. No real model API requests were used for local verification.

A built candidate passed all 46 isolated HTTP cases, covering both client streaming modes, normal text, refusal text, terminal snapshots, partial deltas, sequential/interleaved tools, deferred mixed content, truncation, failed/missing terminals, system strings, URL images, effort, and tool selection. This exercises the actual server binary and captures the outgoing request payload.

Go 1.27.3 in the existing `golang:1.27.3-alpine3.24` image is used for local validation. The repository CI toolchain and native dynamic-plugin build are not implied by this result.

Full-suite and independent-review results are recorded separately in `clirelay-compatibility-verification.md` when verification is complete. Do not treat focused test or HTTP-fixture success as a claim that every upstream test passed.

## Running locally

Use the normal project configuration and commands in `AGENTS.md`. Keep configuration and real OAuth/API credentials outside version control. Build from this branch rather than mounting an old CliRelay binary; CliRelay binaries and data stores are not interchangeable with this v8 server.

This work does not authorize production deployment, a Git commit, or a push.
