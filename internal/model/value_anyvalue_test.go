package model

import "testing"

// TestAnyValueProtoRoundTrip proves the full AnyValue (scalars, arrays, nested
// kvlists, and arbitrary nesting) survives the proto encode/decode used by the
// store without loss. See #259.
func TestAnyValueProtoRoundTrip(t *testing.T) {
	cases := []Value{
		StringValue("linux"),
		IntValue(8),
		DoubleValue(1.5),
		BoolValue(true),
		ArrayValue([]Value{StringValue("a"), IntValue(2), BoolValue(false)}),
		KvlistValue([]KeyValue{
			{Key: "mtu", Value: IntValue(1500)},
			{Key: "addrs", Value: ArrayValue([]Value{StringValue("10.0.0.1"), StringValue("10.0.0.2")})},
		}),
		// deep nesting: array of kvlists, each with a nested array
		ArrayValue([]Value{
			KvlistValue([]KeyValue{{Key: "k", Value: ArrayValue([]Value{IntValue(1), IntValue(2)})}}),
		}),
	}
	for i, v := range cases {
		got := valueFromProto(v.toProto())
		if got.canonical() != v.canonical() {
			t.Errorf("case %d: round-trip changed the value:\n got %s\nwant %s", i, got.canonical(), v.canonical())
		}
		if got.Kind() != v.Kind() {
			t.Errorf("case %d: kind %d != %d", i, got.Kind(), v.Kind())
		}
	}
}

// TestAnyValueCanonicalDeterministic checks that canonical encoding is stable
// against map ordering (kvlist keys sorted) and unambiguous across nesting
// shapes — the property change-detection and identity hashing rely on.
func TestAnyValueCanonicalDeterministic(t *testing.T) {
	a := KvlistValue([]KeyValue{
		{Key: "x", Value: IntValue(1)},
		{Key: "y", Value: IntValue(2)},
	})
	b := KvlistValue([]KeyValue{ // same content, reversed order
		{Key: "y", Value: IntValue(2)},
		{Key: "x", Value: IntValue(1)},
	})
	if a.canonical() != b.canonical() {
		t.Errorf("kvlist canonical must be order-independent:\n%s\n%s", a.canonical(), b.canonical())
	}

	// Two different shapes must not collide: ["12"] vs ["1","2"].
	one := ArrayValue([]Value{StringValue("12")})
	two := ArrayValue([]Value{StringValue("1"), StringValue("2")})
	if one.canonical() == two.canonical() {
		t.Errorf("distinct array shapes collided: %s", one.canonical())
	}
}

// TestAnyValueDisplayJSON pins the human/LLM rendering: scalars stay untyped,
// composites render as compact JSON with sorted kvlist keys.
func TestAnyValueDisplayJSON(t *testing.T) {
	v := KvlistValue([]KeyValue{
		{Key: "mtu", Value: IntValue(1500)},
		{Key: "addrs", Value: ArrayValue([]Value{StringValue("10.0.0.1"), StringValue("10.0.0.2")})},
	})
	const want = `{"addrs":["10.0.0.1","10.0.0.2"],"mtu":1500}`
	if got := v.Display(); got != want {
		t.Errorf("Display() = %s, want %s", got, want)
	}
	if got := StringValue("plain").Display(); got != "plain" {
		t.Errorf("scalar Display() = %q, want plain", got)
	}
}
