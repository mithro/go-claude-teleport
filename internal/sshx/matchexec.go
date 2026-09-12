package sshx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// defaultExecTimeout bounds one Match exec guard. OpenSSH waits forever; a
// teleport runs unattended, so a guard that never answers must not become a
// job that never starts.
const defaultExecTimeout = 5 * time.Second

// defaultExecAllow is the set of commands a Match exec guard may run without
// the caller opting in. Every one of them reports state and changes none, so
// running it to decide whether a config block applies cannot do anything the
// user's own ssh would not have done anyway. Extend it with
// ConfigOptions.ExecAllow, which comes from the command line — never from the
// config file, which would defeat the point of having a list at all.
var defaultExecAllow = map[string]bool{
	"[": true, "false": true, "grep": true, "hostname": true,
	"id": true, "test": true, "true": true, "uname": true,
}

// execMeta are the characters that only mean something to a shell. We run the
// guard directly and never through one, so a command containing any of them
// is refused rather than run with the character taken literally — which would
// quietly answer a different question than the one the config asked. "%" is
// here because OpenSSH expands %h and friends against the host being matched,
// and this config is decoded once for every host a run touches.
const execMeta = "|&;<>()$`*?~#{}\\\n\r\t%'"

// matchCriteria is the set of Match criteria OpenSSH defines. The in-process
// parser (spec §4.2 reads ~/.ssh/config in the binary, it does not shell out
// to ssh) implements "all" and "host"; this package adds "exec". A guard
// built from anything else cannot be decided here.
var matchCriteria = map[string]bool{
	"all": true, "canonical": true, "exec": true, "final": true,
	"host": true, "localnetwork": true, "localuser": true,
	"originalhost": true, "tagged": true, "user": true,
}

// matchVerdict says what sanitizeConfig should do with a Match line.
type matchVerdict int

const (
	matchKeep    matchVerdict = iota // the parser decides this guard itself
	matchRewrite                     // decided here; replace the guard with rewritten
	matchDrop                        // the guard is false, or cannot be decided
)

// splitArgs splits an ssh_config value into whitespace-separated tokens,
// honouring the double quotes OpenSSH uses to hold a Match exec command
// together. It reports false on an unterminated quote.
func splitArgs(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	quoted, started := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, started = !quoted, true
		case !quoted && (r == ' ' || r == '\t'):
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quoted {
		return nil, false
	}
	if started {
		out = append(out, cur.String())
	}
	return out, true
}

// execAllowed reports whether name may be run. A single "none" anywhere in
// extra refuses everything, built-ins included.
func execAllowed(name string, extra []string) bool {
	for _, e := range extra {
		if strings.EqualFold(strings.TrimSpace(e), "none") {
			return false
		}
	}
	if defaultExecAllow[name] {
		return true
	}
	for _, e := range extra {
		if strings.TrimSpace(e) == name {
			return true
		}
	}
	return false
}

// runExecGuard runs one allow-listed Match exec command and reports whether it
// exited 0. why is non-empty when the command was not run at all, and is the
// whole reason to show the user.
func runExecGuard(cmd string, o ConfigOptions) (succeeded bool, why string) {
	refuse := func(format string, a ...any) (bool, string) {
		return false, fmt.Sprintf("Match exec %q refused: ", cmd) + fmt.Sprintf(format, a...)
	}
	argv, ok := splitArgs(cmd)
	if !ok {
		return refuse("it has an unterminated quote")
	}
	if len(argv) == 0 {
		return refuse("it is empty")
	}
	for _, arg := range argv {
		if i := strings.IndexAny(arg, execMeta); i >= 0 {
			return refuse("%q would need a shell, and one is never used here", string(arg[i]))
		}
	}
	name := argv[0]
	if strings.ContainsRune(name, '/') {
		return refuse("only a bare command name is run, not the path %q", name)
	}
	if !execAllowed(name, o.ExecAllow) {
		return refuse("%q is not on the allow list of commands a Match exec may run", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return refuse("%v", err)
	}

	timeout := o.ExecTimeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Nothing is read from the guard but its exit status: no stdin to read,
	// and its output goes nowhere.
	err = exec.CommandContext(ctx, path, argv[1:]...).Run()
	if ctx.Err() != nil {
		return false, fmt.Sprintf("Match exec %q: no answer within %s", cmd, timeout)
	}
	return err == nil, ""
}

// evalMatch decides a Match guard. For matchRewrite, rewritten is the guard to
// put in its place — what is left once the parts decided here are taken out,
// which the parser can then evaluate per lookup. why is a reason to warn, and
// is empty when the outcome is a correct decision rather than something that
// could not be done: a guard that simply evaluates false is not a complaint.
func evalMatch(rest string, o ConfigOptions) (v matchVerdict, rewritten, why string) {
	drop := func(format string, a ...any) (matchVerdict, string, string) {
		return matchDrop, "", fmt.Sprintf(format, a...)
	}
	tokens, ok := splitArgs(rest)
	if !ok {
		return drop("Match guard %q has an unterminated quote", strings.TrimSpace(rest))
	}
	if len(tokens) == 0 {
		return drop("Match with no criterion")
	}

	var hosts, execs []string
	var negated []bool
	for i := 0; i < len(tokens); i++ {
		crit := strings.ToLower(tokens[i])
		not := strings.HasPrefix(crit, "!")
		crit = strings.TrimPrefix(crit, "!")
		switch crit {
		case "all":
			// OpenSSH requires "all" to stand alone, and the parser cannot
			// negate it.
			if not || len(tokens) != 1 {
				return drop("unsupported compound Match guard %q", strings.TrimSpace(rest))
			}
		case "host":
			// A negated host pattern is one the parser cannot express.
			if not || i+1 >= len(tokens) {
				return drop("unsupported Match guard %q", strings.TrimSpace(rest))
			}
			i++
			hosts = append(hosts, tokens[i])
		case "exec":
			if i+1 >= len(tokens) {
				return drop("Match exec with no command")
			}
			i++
			execs = append(execs, tokens[i])
			negated = append(negated, not)
		default:
			if matchCriteria[crit] {
				return drop("unsupported Match criterion %q", tokens[i])
			}
			return drop("unsupported compound Match guard %q", strings.TrimSpace(rest))
		}
	}

	if len(execs) == 0 {
		return matchKeep, "", "" // "all", or plain "host <patterns>"
	}
	for i, cmd := range execs {
		succeeded, why := runExecGuard(cmd, o)
		if why != "" {
			return matchDrop, "", why
		}
		if succeeded == negated[i] {
			return matchDrop, "", "" // decided, and false: nothing to report
		}
	}
	if len(hosts) > 0 {
		return matchRewrite, "host " + strings.Join(hosts, " "), ""
	}
	return matchRewrite, "all", ""
}
