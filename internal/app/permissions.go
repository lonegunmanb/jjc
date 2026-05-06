package app

import (
	copilot "github.com/github/copilot-sdk/go"
)

// approveAllUserIntent is the OnPermissionRequest handler we install on
// every worker session.
//
// History: SDK v0.2.2's copilot.PermissionHandler.ApproveAll returned
// Kind="approved" (the SDK's internal decision enum), which the embedded
// `@github/copilot` CLI runtime rejected as "unexpected user permission
// response". The CLI only accepts user-intent values:
// approve-once, reject, user-not-available.
//
// SDK v0.3.0 fixes the upstream bug by redirecting the
// PermissionRequestResultKindApproved constant to "approve-once". So
// copilot.PermissionHandler.ApproveAll is now safe to use directly.
//
// We still keep this explicit handler (and its regression test) because:
//   - it documents the intent in code so future SDK regressions in the
//     ApproveAll constant value would be caught by the regression test
//     in permissions_test.go before they hit production;
//   - returning the SDK constant (rather than a hand-typed string) keeps
//     us safe against word-list churn in the CLI runtime.
//
// NOTE: do NOT use raw strings like "approve-for-session" or
// "approve-permanently" here — only "approve-once", "reject",
// "user-not-available", "no-result" are documented as accepted by the
// CLI in this SDK version. Anything else may either be rejected
// outright (v0.2.2-style "unexpected user permission response") or
// hang the session (no completion event ever fires).
func approveAllUserIntent(_ copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	return copilot.PermissionRequestResult{
		Kind: copilot.PermissionRequestResultKindApproved, // = "approve-once" in v0.3.0
	}, nil
}
