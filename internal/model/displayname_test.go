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
		// The field always answers: an entity identified by its own name still
		// has a name to show, and a caller that had to branch on "empty" would
		// be writing the fallback this field exists to remove. LabelName is what
		// drops the duplicate, and only for the label.
		name: "identifying key still yields a name",
		e:    ent(TypeHost, []KeyValue{kv("host.name", "web-server-1")}, nil),
		want: "web-server-1",
	}, {
		name: "endpoint name is composed from address and port",
		e: ent(TypeNetworkEndpoint, []KeyValue{
			kv("server.address", "10.0.0.5"), kv("server.port", "5432")}, nil),
		want: "10.0.0.5:5432",
	}, {
		// A bare IPv6 literal followed by :port reads as one more group, so the
		// address is bracketed.
		name: "ipv6 endpoint is bracketed",
		e: ent(TypeNetworkEndpoint, []KeyValue{
			kv("server.address", "2a01:db8::1"), kv("server.port", "443")}, nil),
		want: "[2a01:db8::1]:443",
	}, {
		name: "endpoint without a port is the address alone",
		e:    ent(TypeNetworkEndpoint, []KeyValue{kv("server.address", "10.0.0.5")}, nil),
		want: "10.0.0.5",
	}, {
		// Its identity is one composed string carrying a host uuid, and nothing
		// observed on it reads better. Empty is the honest answer.
		name: "listener has no readable name",
		e:    ent(TypeServiceListener, []KeyValue{kv("service.endpoint", "6ccc0dcc:135/tcp")}, nil),
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
		name: "an unmapped key on a type is not borrowed",
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

// LabelName drops only what the label would print twice; the field keeps it.
func TestLabelNameDropsTheDuplicate(t *testing.T) {
	byName := ent(TypeHost, []KeyValue{kv("host.name", "web-server-1")}, nil)
	if got := DisplayName(byName); got != "web-server-1" {
		t.Fatalf("DisplayName = %q, want web-server-1", got)
	}
	if got := LabelName(byName); got != "" {
		t.Fatalf("LabelName = %q, want empty to avoid a duplicated label", got)
	}

	byID := ent(TypeHost, []KeyValue{kv("host.id", "6ccc0dcc")}, []KeyValue{kv("host.name", "dash172")})
	if got := LabelName(byID); got != "dash172" {
		t.Fatalf("LabelName = %q, want dash172", got)
	}
}
