package entflow

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Codec[In] serializes and deserializes a flow's input value. The interface
// is deliberately parameterized on the input type rather than requiring the
// input type to implement anything (D-17) — inputs originate from RPC, cron,
// CLI, tests, or another flow's outbox event, and an interface-on-the-type
// design would be unimplementable for protobuf messages and third-party
// structs the caller does not own.
type Codec[In any] interface {
	Marshal(In) ([]byte, error)
	Unmarshal([]byte) (In, error)
}

// JSONCodec is the default Codec[In], backed by encoding/json. New[In]
// resolves to it whenever no WithCodec option is supplied.
type JSONCodec[In any] struct{}

// Marshal encodes v as JSON.
func (JSONCodec[In]) Marshal(v In) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("entflow: marshal %T: %w", v, err)
	}
	return b, nil
}

// Unmarshal decodes b into a zero value of In. When In is itself a pointer
// type the zero value is nil, so decoding targets the address of the local
// variable and lets encoding/json allocate — the value is never dereferenced
// before decoding.
//
// On error the message names the target type via %T but never embeds the raw
// input bytes (T-01-05): a serialized flow input can carry PII or payment
// details, and error strings reach logs and OTel span attributes.
func (JSONCodec[In]) Unmarshal(b []byte) (In, error) {
	var v In
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("entflow: unmarshal into %T: %w", v, err)
	}
	return v, nil
}

// WithCodec overrides the default JSON codec used by New[In] for the
// constructed flow. The supplied codec's input type must match the flow's
// input type In; a mismatch panics at New's call site, a declaration-time
// programming mistake caught the first time the schema package loads.
func WithCodec[In any](c Codec[In]) FlowOption {
	return func(cfg *flowConfig) {
		cfg.codec = c
	}
}

// resolveCodec returns the codec configured on cfg, asserted back to
// Codec[In], falling back to JSONCodec[In]{} when no WithCodec option was
// applied. A codec supplied for a mismatched input type panics with a
// descriptive error naming both types.
func resolveCodec[In any](cfg *flowConfig) Codec[In] {
	if cfg.codec == nil {
		return JSONCodec[In]{}
	}
	c, ok := cfg.codec.(Codec[In])
	if !ok {
		panic(fmt.Errorf("entflow: WithCodec supplied a codec for a different input type (want entflow.Codec[%s])", reflect.TypeFor[In]()))
	}
	return c
}
