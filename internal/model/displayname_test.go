package model

import "testing"

func ent(typ string, identity, attrs []KeyValue) Entity {
	return Entity{Type: typ, Identity: identity, Attributes: attrs}
}

func kv(k, v string) KeyValue { return KeyValue{Key: k, Value: StringValue(v)} }

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name string
		e    Entity
		want string
	}{{
		// The case that motivated this: 21 production hosts whose identity is a
		// UUID and whose host.name is present on every one of them.
		name: "host falls back to host.name when identity is the uuid",
		e:    ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, []KeyValue{kv("host.name", "dash172")}),
		want: "dash172",
	}, {
		// Repeating it would pad every label of that type with a duplicate, and
		// would change labels that are already legible.
		name: "no name when the key is itself identifying",
		e:    ent(TypeHost, []KeyValue{kv("host.name", "web-server-1")}, nil),
		want: "",
	}, {
		name: "container prefers container.name",
		e: ent(TypeContainer, []KeyValue{kv("container.id", "b5183e6e")},
			[]KeyValue{kv("compose.service", "senhub-ping"), kv("container.name", "senhub-ping-1")}),
		want: "senhub-ping-1",
	}, {
		name: "container falls through to compose.service",
		e:    ent(TypeContainer, []KeyValue{kv("container.id", "b5183e6e")}, []KeyValue{kv("compose.service", "senhub-ping")}),
		want: "senhub-ping",
	}, {
		name: "service instance uses service.name",
		e:    ent(TypeServiceInstance, []KeyValue{kv("service.instance.id", "ef5c0135")}, []KeyValue{kv("service.name", "senhub-agent")}),
		want: "senhub-agent",
	}, {
		name: "a type with no display key has none",
		e:    ent(TypeServiceListener, []KeyValue{kv("service.endpoint", "h1:443/tcp")}, []KeyValue{kv("service.name", "nginx")}),
		want: "",
	}, {
		name: "absent attribute yields none",
		e:    ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, nil),
		want: "",
	}, {
		name: "empty value is not a name",
		e:    ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, []KeyValue{kv("host.name", "")}),
		want: "",
	}, {
		name: "unregistered type yields none",
		e:    ent("something.else", []KeyValue{kv("k", "v")}, []KeyValue{kv("host.name", "dash172")}),
		want: "",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DisplayName(c.e); got != c.want {
				t.Fatalf("DisplayName = %q, want %q", got, c.want)
			}
		})
	}
}

// A display name must never reach identity: it drifts (a host is renamed while
// its host.id does not), and a drifting value used as a key is how two systems
// end up describing the same thing under names that no longer join.
func TestDisplayNameDoesNotAffectIdentity(t *testing.T) {
	before := ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, []KeyValue{kv("host.name", "dash172")})
	after := ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, []KeyValue{kv("host.name", "dash172-renamed")})

	if DisplayName(before) == DisplayName(after) {
		t.Fatal("the fixture must actually change the display name")
	}
	if before.IdentityHash() != after.IdentityHash() {
		t.Fatalf("renaming changed the identity fingerprint: %s vs %s", before.IdentityHash(), after.IdentityHash())
	}
}
