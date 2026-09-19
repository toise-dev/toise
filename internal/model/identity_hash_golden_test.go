package model

import "testing"

// The identity hash is STORED, not just computed: it keys the byHash and
// tombstone indexes, and it is the key operator annotations live under. A
// change to the canonical encoding would therefore orphan every annotation and
// break resurrection on upgrade — silently, since every property test here
// (determinism, distinctness) would still pass.
//
// These values were produced by the encoding as it shipped. They are a contract
// with data already on disk, not an implementation detail: if one changes, the
// encoding changed, and that needs a migration rather than a new golden.
func TestIdentityHashEncodingIsFrozen(t *testing.T) {
	cases := []struct {
		name string
		e    Entity
		want string
	}{
		{
			name: "single string key",
			e:    Entity{Type: "host", Identity: []KeyValue{{Key: "host.id", Value: StringValue("6a6d1121-4a85-4e64-a222-746f7bc9c04c")}}},
			want: "host:c43f05d286094e40204660d8ac47eae3",
		},
		{
			name: "two keys, given out of sorted order",
			e: Entity{Type: "service.listener", Identity: []KeyValue{
				{Key: "service.endpoint", Value: StringValue("h1:80/tcp")},
				{Key: "network.transport", Value: StringValue("tcp")},
			}},
			want: "service.listener:1a012b4f4755c045bb412d5f71608eb6",
		},
		{
			name: "int value keeps its type tag",
			e:    Entity{Type: "container", Identity: []KeyValue{{Key: "container.id", Value: IntValue(42)}}},
			want: "container:60370d13f4848f0941f78dc349c6fee3",
		},
		{
			name: "every scalar kind, more keys than the stack buffer holds",
			e: Entity{Type: "db", Identity: []KeyValue{
				{Key: "db.instance.id", Value: StringValue("pg@h1")},
				{Key: "port", Value: IntValue(5432)},
				{Key: "tls", Value: BoolValue(true)},
				{Key: "weight", Value: DoubleValue(1.5)},
			}},
			want: "db:c22686805188f805a076409f8ae180d2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.IdentityHash(); got != tc.want {
				t.Errorf("IdentityHash() = %q, want %q\nthe canonical encoding changed: stored hashes on disk no longer match", got, tc.want)
			}
		})
	}
}

// appendCanonical is the allocation-free path IdentityHash takes; it must stay
// byte-identical to canonical(), which the rest of the model still uses.
func TestAppendCanonicalMatchesCanonical(t *testing.T) {
	values := []Value{
		StringValue("plain"), StringValue(""), StringValue("a:b=c,d"),
		IntValue(0), IntValue(-7), DoubleValue(1.5), DoubleValue(-0.25),
		BoolValue(true), BoolValue(false),
		ArrayValue([]Value{StringValue("x"), IntValue(2)}),
		KvlistValue([]KeyValue{{Key: "b", Value: IntValue(1)}, {Key: "a", Value: StringValue("z")}}),
	}
	for _, v := range values {
		if got, want := string(v.appendCanonical(nil)), v.canonical(); got != want {
			t.Errorf("appendCanonical = %q, canonical = %q", got, want)
		}
	}
}
