package mesh

import "fmt"

// Exit codes preserved from bin/ask-agent for drop-in parity.
const (
	ExitUnknownTarget = 2
	ExitDepth         = 3
	ExitHourlyCap     = 4
	ExitLockReclaim   = 5
	ExitLockHeld      = 6
)

// CodedError carries a process exit code up to main so the CLI exits with the
// same status the bash tool did.
type CodedError struct {
	Code int
	Msg  string
}

func (e *CodedError) Error() string { return e.Msg }

func coded(code int, format string, args ...any) *CodedError {
	return &CodedError{Code: code, Msg: fmt.Sprintf(format, args...)}
}
