package main

// Errors and exit codes: every failure becomes a cmdError with a stable code.
// exit=2 means the command line is invalid (unknown command, bad flag, or usage error).
// exit=1 means the command line was valid but the browser, page, or lock failed.

import (
	"errors"
	"fmt"
)

const (
	codeUsage = "usage"
	codeError = "error"

	codeTargetRequired    = "target_required"
	codeTargetTooShort    = "target_too_short"
	codeTargetAmbiguous   = "target_ambiguous"
	codeTargetNotFound    = "target_not_found"
	codeTabNotVisible     = "tab_not_visible"
	codeActivationTimeout = "activation_timeout"
	codeLockBusy          = "lock_busy"
	codeLockUnsupported   = "lock_unsupported"
	codeLabelAmbiguous    = "label_ambiguous"
	codeLabelNotFound     = "label_not_found"
	codeFrameAmbiguous    = "frame_ambiguous"
	codeFrameNotFound     = "frame_not_found"
)

// cmdError is the single error shape: code for machine branching, msg for humans,
// kv for extra fields flattened into the JSON error, and exit for the process code.
type cmdError struct {
	code string
	msg  string
	kv   map[string]any
	exit int
}

func (e *cmdError) Error() string { return e.msg }

func fail(code, msg string, kv map[string]any) *cmdError {
	return &cmdError{code: code, msg: msg, kv: kv, exit: 1}
}

// usagef marks an invalid command line: exit 2, code=usage.
func usagef(format string, a ...any) *cmdError {
	return &cmdError{code: codeUsage, msg: fmt.Sprintf(format, a...), exit: 2}
}

// asCmdError normalizes any error into cmdError. Unclassified errors keep their text with code=error.
// A wrapped cmdError keeps its code and uses the outer message for context.
func asCmdError(err error) *cmdError {
	var ce *cmdError
	if errors.As(err, &ce) {
		if ce.msg == err.Error() {
			return ce
		}
		out := *ce
		out.msg = err.Error()
		return &out
	}
	return &cmdError{code: codeError, msg: err.Error(), exit: 1}
}
