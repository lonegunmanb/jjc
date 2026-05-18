package tunnel

import "testing"

func TestParseQuickTunnelURL(t *testing.T) {
	line := `2026-05-18T03:14:15Z INF | https://formal-sent-saw-gpl.trycloudflare.com  |`
	got, ok := ParseQuickTunnelURL(line)
	if !ok {
		t.Fatal("expected URL match")
	}
	if got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
		t.Fatalf("got %q", got)
	}
}

func TestParseQuickTunnelURLNoMatch(t *testing.T) {
	if got, ok := ParseQuickTunnelURL("no tunnel here"); ok || got != "" {
		t.Fatalf("got (%q, %v), want no match", got, ok)
	}
}

func TestNormalizePublicURLAppendsExactlyOneSlash(t *testing.T) {
	for _, raw := range []string{
		"https://formal-sent-saw-gpl.trycloudflare.com",
		" https://formal-sent-saw-gpl.trycloudflare.com/ ",
		"https://formal-sent-saw-gpl.trycloudflare.com////",
	} {
		if got := NormalizePublicURL(raw); got != "https://formal-sent-saw-gpl.trycloudflare.com/" {
			t.Fatalf("NormalizePublicURL(%q) = %q", raw, got)
		}
	}
}
