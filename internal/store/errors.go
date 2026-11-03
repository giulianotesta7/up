package store

import "errors"

var (
	ErrNotFound   = errors.New("not found")
	ErrExists     = errors.New("already exists")
	ErrValidation = errors.New("validation failed")
)

type classifiedError struct {
	kind error
	err  error
}

func (e *classifiedError) Error() string        { return e.err.Error() }
func (e *classifiedError) Unwrap() error        { return e.err }
func (e *classifiedError) Is(target error) bool { return target == e.kind }
func classify(kind, err error) error            { return &classifiedError{kind: kind, err: err} }
