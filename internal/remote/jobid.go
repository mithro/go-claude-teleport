package remote

import "github.com/mithro/go-claude-teleport/internal/job"

// A job id arrives from the far side of an ssh pipe and this host joins it
// verbatim into jobs/<id> and staging/<id>, then writes to — or removes —
// what it finds there. Ruling R-P3-23n therefore puts the check in the one
// place every op's arguments already pass through: decode (server.go)
// validates any args value implementing jobIDCarrier BEFORE the handler
// runs, so no Endpoint method is ever called with an id that could escape
// the data dir. Local re-checks for itself (checkJobID below), because it
// is reachable without the wire dispatch too (ServeStream, a chained
// endpoint, this process's own code).
//
// Every args type carrying a wire-supplied job id implements this
// interface; internal/remote's dispatch_args_test.go enumerates all the
// dispatched ops by reflection and fails if one carries a job id without
// implementing it, so a future op cannot quietly skip the check.
type jobIDCarrier interface {
	// wireJobIDs returns every job id this args value carries, or none
	// when the field it lives in is absent (a nil payload, which the
	// handler rejects on its own terms).
	wireJobIDs() []string
}

func (a ManifestDiffArgs) wireJobIDs() []string  { return []string{a.JobID} }
func (a InstallExtrasArgs) wireJobIDs() []string { return []string{a.JobID} }
func (a InstallArgs) wireJobIDs() []string       { return []string{a.JobID} }
func (a GitAttachArgs) wireJobIDs() []string     { return []string{a.JobID} }
func (a CaptureArgs) wireJobIDs() []string       { return []string{a.JobID} }
func (a StartClaudeArgs) wireJobIDs() []string   { return []string{a.JobID} }
func (a JournalGetArgs) wireJobIDs() []string    { return []string{a.JobID} }
func (a RecordArgs) wireJobIDs() []string        { return []string{a.JobID} }
func (a buildManifestArgs) wireJobIDs() []string { return []string{a.JobID} }
func (a cleanupArgs) wireJobIDs() []string       { return []string{a.JobID} }
func (a removeJobArgs) wireJobIDs() []string     { return []string{a.JobID} }

// JournalPutArgs' job id is the journal's own ID: that is what JournalPut
// joins into jobs/<id>. A nil journal carries nothing to validate (the
// handler's Endpoint rejects it as a usage error).
func (a JournalPutArgs) wireJobIDs() []string {
	if a.Journal == nil {
		return nil
	}
	return []string{a.Journal.ID}
}

// checkJobID turns job.ValidateID's verdict into a protocol error, so a
// bad id reads the same to the caller whether it was caught at the wire
// boundary or by Local itself.
func checkJobID(jobID string) error {
	if err := job.ValidateID(jobID); err != nil {
		return &Error{Code: "usage", Message: err.Error()}
	}
	return nil
}
