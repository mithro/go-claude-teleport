package session

import "testing"

func TestParseID(t *testing.T) {
	id, err := ParseID("3F9C2B7E-5A14-4D8E-9B21-7C0E5D6A8F13")
	if err != nil {
		t.Fatal(err)
	}
	if id != "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13" {
		t.Fatalf("not lower-cased: %q", id)
	}
	if id.Short() != "3f9c2b7e" {
		t.Fatalf("Short = %q", id.Short())
	}
	for _, bad := range []string{"", "3f9c2b7e", "3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f1", "not-a-uuid-at-all-really-not-a-uuid"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
	if !IsUUID("3f9c2b7e-5a14-4d8e-9b21-7c0e5d6a8f13") || IsUUID("3f9c2b7e") {
		t.Fatal("IsUUID")
	}
}
