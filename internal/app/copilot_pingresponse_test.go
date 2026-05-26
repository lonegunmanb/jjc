package app

import (
	"encoding/json"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

// These tests pin the Copilot SDK contract that CopilotRunner.Start
// (runner.go) relies on: the SDK must accept both the JSON-number and
// JSON-string wire shapes that github/copilot-cli emits for
// result.timestamp on its ping reply.
//
// Upstream `github.com/github/copilot-sdk/go` v0.3.0 declares
// PingResponse.Timestamp as int64 with the default JSON unmarshaller,
// so it crashes on Linux 1.0.51 with:
//
//   json: cannot unmarshal string into Go struct field PingResponse.timestamp of type int64
//
// See:
//   - https://github.com/github/copilot-sdk/issues/1356
//   - https://github.com/github/copilot-cli/issues/3444
//   - https://github.com/lonegunmanb/copilot-sdk/issues/1
//
// JJC swaps in the lonegunmanb/copilot-sdk fork (v0.3.2) via a `replace`
// directive in go.mod to unblock Linux today. These tests are the
// Red→Green proof that the swap actually fixes runner.Start on Linux.
//
// If the SDK ever loses this tolerance again (e.g. someone drops the
// replace directive before upstream merges the fix), these tests will
// fail loudly instead of users hitting copilot_runner_start_failed at
// startup.

func TestPingResponseDecodesNumericTimestamp(t *testing.T) {
	const raw = `{"message":"pong","timestamp":1779352370134,"protocolVersion":3}`
	var resp copilot.PingResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal numeric timestamp: %v", err)
	}
	if resp.Timestamp != 1779352370134 {
		t.Fatalf("Timestamp got %d, want 1779352370134", resp.Timestamp)
	}
	if resp.Message != "pong" {
		t.Fatalf("Message got %q, want %q", resp.Message, "pong")
	}
}

func TestPingResponseDecodesISO8601StringTimestamp(t *testing.T) {
	// Real wire payload captured from github/copilot-cli 1.0.51 on Linux.
	const raw = `{"message":"pong","timestamp":"2026-05-21T08:29:54.042Z","protocolVersion":3}`
	var resp copilot.PingResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal ISO-8601 timestamp (the Linux 1.0.51 wire shape that breaks v0.3.0): %v", err)
	}
	// 2026-05-21T08:29:54.042Z == 1779352194042 ms since epoch.
	if resp.Timestamp != 1779352194042 {
		t.Fatalf("Timestamp got %d, want 1779352194042 (epoch ms)", resp.Timestamp)
	}
	if resp.Message != "pong" {
		t.Fatalf("Message got %q, want %q", resp.Message, "pong")
	}
}

func TestPingResponseDecodesStringifiedEpochTimestamp(t *testing.T) {
	const raw = `{"message":"pong","timestamp":"1779352370134","protocolVersion":3}`
	var resp copilot.PingResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal stringified epoch timestamp: %v", err)
	}
	if resp.Timestamp != 1779352370134 {
		t.Fatalf("Timestamp got %d, want 1779352370134", resp.Timestamp)
	}
}
