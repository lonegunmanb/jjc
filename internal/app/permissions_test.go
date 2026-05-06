package app

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

// TestApproveAllUserIntent_ReturnsCLIAcceptedKind is a regression guard
// for the permission word-list mismatch that bit us on SDK v0.2.2. The
// embedded CLI runtime only accepts "user-intent" values for
// PermissionRequestResult.Kind; returning the SDK's old internal
// "approved" string makes subagent fs/shell tool calls fail with
// "unexpected user permission response". SDK v0.3.0 has fixed the
// upstream constant value, but we keep this test so any future
// regression there (or accidental local change to our handler) is
// caught immediately.
func TestApproveAllUserIntent_ReturnsCLIAcceptedKind(t *testing.T) {
	got, err := approveAllUserIntent(copilot.PermissionRequest{}, copilot.PermissionInvocation{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must be one of the user-intent values the v0.3.0 CLI runtime
	// documents as accepted. NOT raw strings like "approve-for-session"
	// (which hung the session in testing) and NOT the v0.2.2 internal
	// "approved" string (which produced "unexpected user permission
	// response").
	accepted := map[copilot.PermissionRequestResultKind]bool{
		copilot.PermissionRequestResultKindApproved:         true, // "approve-once"
		copilot.PermissionRequestResultKindRejected:         true,
		copilot.PermissionRequestResultKindUserNotAvailable: true,
	}
	if !accepted[got.Kind] {
		t.Fatalf("Kind=%q is not one of the v0.3.0 CLI-accepted user-intent constants; risks 'unexpected user permission response' or session hang", got.Kind)
	}

	// And it must literally be the SDK constant value, not a hand-typed
	// string — protects against churn.
	if string(got.Kind) != "approve-once" && string(got.Kind) != "reject" && string(got.Kind) != "user-not-available" {
		t.Fatalf("Kind=%q has an unrecognised raw value", got.Kind)
	}
}
