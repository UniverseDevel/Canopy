// Package apperr defines Canopy's structured errors. Every error shown to a user or
// written as JSON carries a machine-readable code, an exit code class and, where one
// exists, the gRPC status code.
package apperr

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Class groups error codes by CLI exit code.
type Class int

// Exit code classes (docs/ARCHITECTURE.md, 5.13).
const (
	ClassUsage      Class = 1 // bad flags or input
	ClassConnection Class = 2 // connection or authentication failure
	ClassDevice     Class = 3 // the device returned an error
	ClassSchema     Class = 4 // model or schema error
	ClassInternal   Class = 5 // bug or unexpected failure
)

// Error codes.
const (
	CodeUsage              = "usage"
	CodeUnreachable        = "unreachable"
	CodeTimeout            = "timeout"
	CodeTLS                = "tls"
	CodeAuth               = "auth_failed"
	CodeSSH                = "ssh"
	CodeSSHAuth            = "ssh_auth_failed"
	CodeHostKeyUnknown     = "host_key_unknown"
	CodeHostKeyMismatch    = "host_key_mismatch"
	CodePassphraseRequired = "passphrase_required"
	CodeDevice             = "device_error"
	CodeSchema             = "schema_error"
	CodeNotFound           = "not_found"
	CodeInternal           = "internal"
)

// Error is a structured error.
type Error struct {
	Code     string            `json:"code"`
	Message  string            `json:"message"`
	Hint     string            `json:"hint,omitempty"`
	GRPCCode string            `json:"grpcCode,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Class    Class             `json:"-"`
	Err      error             `json:"-"`
}

func (e *Error) Error() string {
	s := e.Message
	if e.GRPCCode != "" {
		s += " (gRPC " + e.GRPCCode + ")"
	}
	if e.Hint != "" {
		s += "; " + e.Hint
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// New creates an error.
func New(class Class, code, format string, args ...any) *Error {
	return &Error{Class: class, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap creates an error wrapping err, using err's text as the message.
func Wrap(class Class, code string, err error) *Error {
	return &Error{Class: class, Code: code, Message: err.Error(), Err: err}
}

// WithHint sets the hint and returns e.
func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }

// WithDetail adds a detail and returns e.
func (e *Error) WithDetail(k, v string) *Error {
	if e.Details == nil {
		e.Details = map[string]string{}
	}
	e.Details[k] = v
	return e
}

// As converts any error into an *Error. Unknown errors become internal errors.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Wrap(ClassInternal, CodeInternal, err)
}

// FromDevice converts an error returned by a gNMI RPC that reached the device
// (the connection itself worked) into a device error carrying the gRPC code.
func FromDevice(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	st, ok := status.FromError(err)
	if !ok {
		return Wrap(ClassInternal, CodeInternal, err)
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		return &Error{Class: ClassConnection, Code: CodeAuth, Message: st.Message(), GRPCCode: st.Code().String(), Err: err,
			Hint: "check the username and password ($GNMI_USERNAME, $GNMI_PASSWORD)"}
	case codes.Unavailable:
		return &Error{Class: ClassConnection, Code: CodeUnreachable, Message: st.Message(), GRPCCode: st.Code().String(), Err: err}
	case codes.DeadlineExceeded:
		return &Error{Class: ClassConnection, Code: CodeTimeout, Message: st.Message(), GRPCCode: st.Code().String(), Err: err}
	}
	e = &Error{Class: ClassDevice, Code: CodeDevice, Message: st.Message(), GRPCCode: st.Code().String(), Err: err}
	switch st.Code() {
	case codes.NotFound:
		e.Hint = "the path is valid for the device but has no data, or isn't supported"
	case codes.Unimplemented:
		e.Hint = "the device doesn't implement this path, encoding or RPC"
	case codes.InvalidArgument:
		e.Hint = "the device rejected the request; check the path, origin and encoding"
	}
	return e
}
