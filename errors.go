package sdk

import "fmt"

// ErrorCode mirrors the subset of gRPC status codes the SDK needs, without
// importing google.golang.org/grpc (the Native build must stay gRPC-free). The
// gRPC transport maps these codes back to grpc status codes at the server
// boundary (see transport/grpc interceptor).
type ErrorCode int

// Codes used by the SDK/host service surfaces. Values match gRPC's codes.*
// numeric values so the transport can convert 1:1.
const (
	CodeInvalidArgument    ErrorCode = 3
	CodePermissionDenied   ErrorCode = 7
	CodeResourceExhausted  ErrorCode = 8
	CodeFailedPrecondition ErrorCode = 9
	CodeUnimplemented      ErrorCode = 12
)

// StatusError is a coded error usable in the grpc-free core.
type StatusError struct {
	Code ErrorCode
	Msg  string
}

func (e *StatusError) Error() string { return e.Msg }

// Error builds a coded error.
func Error(code ErrorCode, msg string) error {
	return &StatusError{Code: code, Msg: msg}
}

// Errorf builds a formatted coded error.
func Errorf(code ErrorCode, format string, a ...any) error {
	return &StatusError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// CodeOf returns the coded error's code, or 0 when err is not a *StatusError.
func CodeOf(err error) ErrorCode {
	if se, ok := err.(*StatusError); ok {
		return se.Code
	}
	return 0
}
