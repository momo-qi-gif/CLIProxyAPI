# CliRelay compatibility port: verification

Date: 2026-11-04. Repository: `momo-qi-gif/CLIProxyAPI`. Branch: `fix/clirelay-claude-compat`. Base commit: `01b24f1f95dc9b309765212c52b25b25e005ff01`.

## Environment

- Existing local Go image: `golang:1.27.3-alpine3.24`.
- Dependencies were fetched using Go's default module source, then tests ran on an isolated Docker internal network with no public egress.
- Source was mounted writable for the final suite because a watcher test creates a temporary directory relative to its package directory.
- The HTTP service probe ran in a fresh user/network namespace with only loopback enabled. Its upstream and credentials are synthetic local fixtures.
- No production model requests, deployment, Git commit, or push were performed. Only the source binary mount and hash were read on vps20005.

## Final results

| Check | Result |
| --- | --- |
| `go test -p 1 -json -count=1 ./...` | **Exit 1**: 101 packages passed, 29 packages had no tests, one package failed because of the known upstream test below. 22,153 test pass events, 13 conditional skip events, one test failure. Events include parent tests and subtests. |
| Full `internal/runtime/executor` and `internal/runtime/executor/helps` packages after duplex fix | Passed |
| `go vet -p 1 ./...` | Passed, log empty |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" -o <output> ./cmd/server` | Passed |
| Final binary through actual HTTP server to synthetic upstream | **46/46 passed**, both streaming modes |
| `git diff --check` | Passed |

Final local binary: `/home/salio/build/cliproxy-compat-KVKvRmn6/cli-proxy-api-delivery`.

SHA256: `81994fd943c3c0feaae65b6d40e5cd76f540dece43af12f7ab31a7085ed621a8`.

The CGO-disabled build does not verify native dynamic-library plugin support. Go 1.26.10/CI builds, race instrumentation, every nested example module, and real model/provider behavior were not verified.

## Remaining upstream failure

`github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai.TestResponsesInterruptStopsHTTPUpstream`

Failure at `openai_responses_interrupt_test.go:376`: `http upstream was not canceled`.

The same failure was reproduced repeatedly with `-count=10` against an unmodified archive of the exact base commit, as well as the patched working tree. This port does not modify that handler or test. The test checks an atomic flag immediately after an interrupt acknowledgment; the recorded reproduction establishes an upstream baseline failure, not a new failure caused by this port. No unrelated handler/test fix or skip was added to conceal it. The full suite is therefore **not described as all green**.

## Earlier failures resolved or explained

- Adaptive `minimal` effort passthrough regressed during the port because the override whitelist was initially applied to all modes. The original E2E assertion reproduced it. The whitelist now applies only to enabled-budget overrides; adaptive/standalone passthrough is retained. Translator, canonical thinking, and `TestThinkingE2EClaudeAdaptive_Body` pass after the fix.
- `TestPionMediaRelayBridgesAudioAndDataChannel` failed under `--network none`, which supplies no non-loopback interface for its ICE exchange. It passes for both pristine base and patched code on the internal Docker test network, and in the final suite.
- `TestSnapshotAuthFileRelativeAuthDir` failed when source was mounted read-only. It passes after the test environment permits its relative temporary directory; no production change was made for this fixture requirement.
- A duplex terminal test originally accepted a later connection-loss 502 as if it were the intended incomplete-terminal failure. Strengthened assertions first reproduced the false positive. The implementation now forwards the raw `response.incomplete` event before its explicit `content_filter` error, before and after `response.created`; the test and full executor packages pass.

## Conditional skips in final full suite

These skips are reported rather than counted as passes:

- `internal/auth/devin.TestLiveDevinUserStatus`
- `internal/config.TestPrepareConfigFileForUploadReportsSecretItCannotHash/read-only_file`
- `internal/runtime/executor/helps.TestClaudeCodeTLSClientHelloCapture`
- `internal/signature.TestIsValidGeminiThoughtSignature_AgyNativeSamples`
- `internal/signature.TestInspectGrokEncryptedContent_NativeSamples`
- `internal/signature.TestInspectGrokEncryptedContent_RejectsAgyGeminiThoughtSignatures`
- `internal/signature.TestDetectSignatureProvider_NeverClassifiesGrok`
- `internal/signature.TestInspectKimiThinkingSignature_NativeCorpus`
- `internal/signature.TestDetectSignatureProvider_KimiProbeDoesNotDisturbCatalog`
- `internal/watcher.TestRefreshAuthStatePreservesUnreadableFile`
- `internal/watcher.TestFullScanPreservesAuthsInUnreadableDir`
- `internal/watcher/synthesizer.TestFileSynthesizerRecordsIncompleteFiles/unreadable_file`
- `internal/watcher/synthesizer.TestFileSynthesizerRecordsIncompleteFiles/unreadable_directory`

They depend on optional live/corpus/capture prerequisites or effective filesystem permissions in the root test container.

## Review

An independent read-only review examined the work against the exact base commit. Its initial enabled-budget + `minimal` finding was withdrawn after checking the actual source contract: only `low/medium/high/xhigh/max` are added as budget overrides; adaptive/standalone `minimal` remains supported. That review reported no remaining blocking findings for the main port. A final narrow read-only review of the duplex/error-JSON adjustment also reported no blocking findings: the raw incomplete terminal precedes the explicit error for Claude sources, both bootstrap states are covered, and non-Claude Responses and length-limit behavior retain their existing paths. The reviewer did not independently rerun the final suite; the test results above were executed by the implementing session.

## Local evidence

`/home/salio/build/cliproxy-compat-KVKvRmn6/` contains:

- `test-delivery.json`, `test-delivery.stderr`: final full-suite results.
- `vet-delivery.log`: final full-module static check.
- `http-delivery-results/upstream/result.json`: final binary hash, 46 individual HTTP cases and assertions.
- `baseline-interrupt.log`, `patched-interrupt.log`: repeated unmodified/patched interrupt reproductions.
- `baseline-webrtc.log`, `patched-webrtc.log`: isolated-interface WebRTC comparisons.
- Earlier `test-full`, `test-final`, and `test-writable` artifacts are historical and must not replace the final `test-delivery` evidence.
