package job

import "testing"

// TestValidateID is ruling R-P3-23n's charset: a job id is used verbatim to
// build jobs/<id>/ and staging/<id>/ on whichever host receives it over the
// wire, so anything that could escape those directories (a separator, a
// "..", an absolute path, a NUL) — or that is simply absent — must be
// rejected before any filesystem call sees it. Real ids are session uuids
// and inspect-<hex>.
func TestValidateID(t *testing.T) {
	long := ""
	for len(long) < 128 {
		long += "a"
	}
	tests := []struct {
		name string
		id   string
		ok   bool
	}{
		{"session uuid", "3f2a9c1e-7b4d-4e8a-9c6f-1d2e3f4a5b6c", true},
		{"inspect throwaway", "inspect-deadbeefcafef00d", true},
		{"underscore and dot inside", "job_1.part-2", true},
		{"128 bytes", long, true},
		{"129 bytes", long + "a", false},
		{"empty", "", false},
		{"traversal with the inspect prefix", "inspect-../x", false},
		{"traversal", "../x", false},
		{"bare parent", "..", false},
		{"bare dot", ".", false},
		{"leading dot", ".hidden", false},
		{"absolute", "/etc", false},
		{"separator", "a/b", false},
		{"backslash", `a\b`, false},
		{"NUL", "a\x00b", false},
		{"newline", "a\nb", false},
		{"space", "a b", false},
		{"unicode", "jöb", false},
		{"tilde home", "~", false},
		{"dollar", "$HOME", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateID(tt.id)
			if tt.ok && err != nil {
				t.Fatalf("ValidateID(%q) = %v, want nil", tt.id, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("ValidateID(%q) = nil, want an error", tt.id)
			}
		})
	}
}

// TestValidateIDErrorNamesTheID keeps the rejection message useful to a
// human debugging a mistyped id (the empty case has no id to name).
func TestValidateIDErrorNamesTheID(t *testing.T) {
	err := ValidateID("../etc")
	if err == nil {
		t.Fatal("want an error")
	}
	if !contains(err.Error(), "../etc") {
		t.Errorf("error %q does not name the offending id", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
