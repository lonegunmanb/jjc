package app

import "testing"

func TestValidateCardID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"empty", "", false},
		{"trello-real", "5f3a6b8c9d0e1f2a3b4c5d6e", true},
		{"short-but-allowed", "abc", true},
		{"hyphen-and-underscore", "card_42-test", true},
		{"too-short", "ab", false},
		{"too-long", string(make([]byte, 65)), false},
		{"slash", "abc/def", false},
		{"backslash", `abc\def`, false},
		{"dotdot", "..foo", false},
		{"dot-only", ".", false},
		{"dotdot-only", "..", false},
		{"colon", "C:foo", false},
		{"absolute-windows", `C:\Windows\System32`, false},
		{"absolute-posix", "/etc/passwd", false},
		{"traversal", "../../etc/passwd", false},
		{"null-byte", "abc\x00def", false},
		{"space", "abc def", false},
		{"unicode", "abc中文", false},
		{"newline", "abc\ndef", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCardID(tc.id)
			if tc.ok && err != nil {
				t.Fatalf("expected %q to be accepted, got %v", tc.id, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected %q to be rejected", tc.id)
			}
			if IsValidCardID(tc.id) != tc.ok {
				t.Fatalf("IsValidCardID(%q) disagreed with ValidateCardID", tc.id)
			}
		})
	}
}
