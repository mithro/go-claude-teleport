package job

import "fmt"

// MaxIDLen bounds a job id, so a hostile peer cannot make this host build a
// pathological path out of one.
const MaxIDLen = 128

// ValidateID rejects any job id that is not a plain, self-contained name.
//
// Every id that arrives over the wire is joined into <dataDir>/jobs/<id>
// and <dataDir>/staging/<id> (Dir/StagingDir) and those directories are
// then written to — and, for inspect's throwaway jobs, removed entirely —
// so an id carrying a separator, a "..", a leading '.' or a NUL could walk
// the receiving host's filesystem outside its own data dir. Only
// [A-Za-z0-9._-] is allowed, which every real id already satisfies: session
// uuids and inspect-<hex>.
//
// Callers treat a non-nil error as "refuse, touch nothing".
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("job id is empty")
	}
	if len(id) > MaxIDLen {
		return fmt.Errorf("job id %q is %d bytes (max %d)", id, len(id), MaxIDLen)
	}
	// Excludes "." and ".." (and dotfiles) outright.
	if id[0] == '.' {
		return fmt.Errorf("job id %q starts with '.'", id)
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; !(c == '-' || c == '.' || c == '_' ||
			('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
			return fmt.Errorf("job id %q contains %q at byte %d (allowed: letters, digits, '.', '_', '-')", id, string(c), i)
		}
	}
	return nil
}
