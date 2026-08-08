package entflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// plainInput declares no entflow methods, proving CORE-08's "any Go type"
// claim: the codec is a construction option, not an interface In must
// implement.
type plainInput struct {
	OrderID string
	Amount  int
	Tags    map[string]string
}

func TestJSONCodecRoundTripStruct(t *testing.T) {
	var codec JSONCodec[plainInput]
	in := plainInput{OrderID: "ord_1", Amount: 42, Tags: map[string]string{"k": "v"}}

	b, err := codec.Marshal(in)
	require.NoError(t, err)

	got, err := codec.Unmarshal(b)
	require.NoError(t, err)
	require.Equal(t, in, got)
}

func TestJSONCodecRoundTripPointer(t *testing.T) {
	var codec JSONCodec[*plainInput]
	in := &plainInput{OrderID: "ord_2", Amount: 7}

	b, err := codec.Marshal(in)
	require.NoError(t, err)

	got, err := codec.Unmarshal(b)
	require.NoError(t, err)
	require.Equal(t, in, got)
}

func TestJSONCodecUnmarshalMalformedBytes(t *testing.T) {
	var codec JSONCodec[plainInput]

	secretLike := `{"OrderID": "not json`
	_, err := codec.Unmarshal([]byte(secretLike))
	require.Error(t, err)
	require.NotContains(t, err.Error(), secretLike)
}

func TestJSONCodecUnmarshalMalformedBytesDoesNotEmbedSecret(t *testing.T) {
	var codec JSONCodec[plainInput]

	// A malformed payload that happens to carry a secret-looking value.
	malformed := []byte(`{"OrderID": "sk_live_super_secret_do_not_leak", `)
	_, err := codec.Unmarshal(malformed)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sk_live_super_secret_do_not_leak")
}

type customCodecInput struct {
	Value string
}

type recordingCodec struct {
	marshalCalled bool
}

func (c *recordingCodec) Marshal(in customCodecInput) ([]byte, error) {
	c.marshalCalled = true
	return []byte("custom:" + in.Value), nil
}

func (c *recordingCodec) Unmarshal(b []byte) (customCodecInput, error) {
	return customCodecInput{Value: string(b)}, nil
}

func TestJSONCodecIsDefault(t *testing.T) {
	f := New[plainInput]("DefaultCodecFlow")
	require.IsType(t, JSONCodec[plainInput]{}, f.codecOf())
}

func TestWithCodecUsesCustomCodec(t *testing.T) {
	custom := &recordingCodec{}
	f := New[customCodecInput]("CustomCodecFlow", WithCodec[customCodecInput](custom))

	got := f.codecOf()
	require.Same(t, custom, got)

	_, err := got.Marshal(customCodecInput{Value: "x"})
	require.NoError(t, err)
	require.True(t, custom.marshalCalled)
}

func TestWithCodecMismatchedTypePanics(t *testing.T) {
	// A codec built for a different input type than New's type parameter must
	// panic at New's call site with a descriptive error naming both types.
	mismatched := &recordingCodec{}

	require.Panics(t, func() {
		New[plainInput]("MismatchedCodecFlow", func(cfg *flowConfig) {
			cfg.codec = mismatched
		})
	})
}
