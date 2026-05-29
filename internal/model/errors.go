package model

import "errors"

// Validation sentinel errors. Callers may test with errors.Is.
var (
	ErrEmptyType          = errors.New("model: empty type")
	ErrUnknownType        = errors.New("model: unknown type")
	ErrNoIdentity         = errors.New("model: entity has no identifying attributes")
	ErrEmptyKey           = errors.New("model: empty attribute key")
	ErrDuplicateKey       = errors.New("model: duplicate attribute key")
	ErrInvalidValue       = errors.New("model: invalid (unset) value")
	ErrEmptyEndpoint      = errors.New("model: relation endpoint is empty")
	ErrChangeTypeUnset    = errors.New("model: change type unspecified")
	ErrChangeTypeMismatch = errors.New("model: change type does not match event kind")
	ErrMissingEntity      = errors.New("model: entity event has no entity")
	ErrMissingRelation    = errors.New("model: relation event has no relation")
	ErrZeroEventTime      = errors.New("model: event_time is zero")
	ErrZeroRecordedAt     = errors.New("model: recorded_at is zero")
	ErrEmptySchemaVersion = errors.New("model: empty schema version")
)
