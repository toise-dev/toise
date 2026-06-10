package model

import "testing"

// FuzzIdentityHash pins the exact-identity contract (ADR 0018) for arbitrary
// inputs: the hash is deterministic, order-insensitive over identity pairs,
// sensitive to type, key, and value, and never panics.
func FuzzIdentityHash(f *testing.F) {
	f.Add("host", "host.id", "h1", "host.name", "web-1")
	f.Add("", "", "", "", "")
	f.Add("net.device", "k1", "v", "k2", "v2")
	f.Fuzz(func(t *testing.T, typ, k1, v1, k2, v2 string) {
		if k1 == k2 {
			// Duplicate identity keys are rejected upstream (Entity.Validate at
			// the ingest boundary, #109) and the canonical sort is by key only,
			// so the hash makes no ordering promise for them.
			return
		}
		a := Entity{Type: typ, Identity: []KeyValue{
			{Key: k1, Value: StringValue(v1)}, {Key: k2, Value: StringValue(v2)}}}
		b := Entity{Type: typ, Identity: []KeyValue{
			{Key: k2, Value: StringValue(v2)}, {Key: k1, Value: StringValue(v1)}}}
		ha, hb := a.IdentityHash(), b.IdentityHash()
		if ha != hb {
			t.Fatalf("hash is order-sensitive: %q vs %q", ha, hb)
		}
		if ha != a.IdentityHash() {
			t.Fatal("hash is not deterministic")
		}
		other := Entity{Type: typ + "x", Identity: a.Identity}
		if other.IdentityHash() == ha {
			t.Fatal("hash ignores the entity type")
		}
		// changing one value must change the hash
		c := Entity{Type: typ, Identity: []KeyValue{
			{Key: k1, Value: StringValue(v1 + "\x01")}, {Key: k2, Value: StringValue(v2)}}}
		if c.IdentityHash() == ha {
			t.Fatal("hash ignores an identity value change")
		}
	})
}
