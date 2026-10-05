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
		// Nothing observed on it: empty is the honest answer, not a dressed-up id.
		name: "listener with nothing observed has no name",
		e:    ent(TypeServiceListener, []KeyValue{kv("service.endpoint", "6ccc0dcc:135/tcp")}, nil),
		want: "",
	}, {
		// But when the producer did observe the process behind the socket, that
		// is the readable part of an endpoint made of a uuid and a port.
		name: "listener named by the process behind it",
		e: ent(TypeServiceListener, []KeyValue{kv("service.endpoint", "6ccc0dcc:443/tcp")},
			[]KeyValue{kv("process.executable.name", "nginx")}),
		want: "nginx",
	}, {
		// The key an SNMP poll actually fills. Looking only for the keys we
		// imagined, and concluding the name was absent, is how this was missed:
		// the data was there under a key nobody queried (#364).
		name: "network device named by the polled sysName",
		e: ent(TypeNetworkDevice, []KeyValue{kv("network.device.id", "engine:80001f880461636331")},
			[]KeyValue{kv("sys.name", "acc1")}),
		want: "acc1",
	}, {
		// An operator-set name outranks the polled one.
		name: "an explicit device name wins over sys.name",
		e: ent(TypeNetworkDevice, []KeyValue{kv("network.device.id", "engine:x")},
			[]KeyValue{kv("sys.name", "acc1"), kv("network.device.name", "acces-1")}),
		want: "acces-1",
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

// A producer migrating to the semconv spelling must not go dark, and one that
// has not migrated must not either: both are live at once during a rollout, and
// the retention window holds pre-migration observations long after it.
func TestInterfaceNameBothSpellings(t *testing.T) {
	semconv := ent(TypeNetworkInterface,
		[]KeyValue{kv("network.device.id", "engine:x"), kv("network.interface.name", "Gi1/0/1")}, nil)
	if got := DisplayName(semconv); got != "Gi1/0/1" {
		t.Fatalf("semconv spelling: DisplayName = %q, want Gi1/0/1", got)
	}
	legacy := ent(TypeNetworkInterface,
		[]KeyValue{kv("network.device.id", "engine:x"), kv("interface.name", "Gi1/0/1")}, nil)
	if got := DisplayName(legacy); got != "Gi1/0/1" {
		t.Fatalf("legacy spelling: DisplayName = %q, want Gi1/0/1", got)
	}

	// They are NOT the same entity: the key is part of the identity, so the
	// migration re-mints once. Pinning it here so nobody later "fixes" it into
	// a silent merge, which is the one thing exact identity forbids.
	if semconv.IdentityHash() == legacy.IdentityHash() {
		t.Fatal("the two spellings must remain distinct identities")
	}
}

// TestEveryRegisteredTypeDecidesOnADisplayName is the invariant that outlives the
// three types it was written for. Before it existed, displayNameKeys held ten of
// the fourteen registered types and nothing failed: network.address, db and
// network.segment shipped in 0.18.0 with no display name and no test protesting,
// because the suite asserted hand-written cases and never enumerated the
// registry. A type added later fell in the same hole silently.
//
// The point is not that every type has a name. It is that having none is written
// down, in displayNameNone, with the reason — so the next type added forces a
// decision instead of inheriting an omission.
func TestEveryRegisteredTypeDecidesOnADisplayName(t *testing.T) {
	for typ := range entityTypes {
		_, keyed := displayNameKeys[typ]
		_, composed := displayNameComposed[typ]
		reason, none := displayNameNone[typ]
		switch {
		case none && (keyed || composed):
			t.Errorf("%s is in displayNameNone and also has a name source: decide one way", typ)
		case none && reason == "":
			t.Errorf("%s is in displayNameNone with no reason: the reason is the decision", typ)
		case !keyed && !composed && !none:
			t.Errorf("%s has no display name and does not declare that it has none: "+
				"add it to displayNameKeys, to displayNameComposed, or to displayNameNone with a reason", typ)
		}
	}
	for typ := range displayNameNone {
		if _, ok := entityTypes[typ]; !ok {
			t.Errorf("displayNameNone names %s, which is not a registered entity type", typ)
		}
	}
}

func TestDisplayNameOfTheTypesThatHadNoneIn0180(t *testing.T) {
	cases := []struct {
		name     string
		entity   Entity
		want     string
		wantLbl  string
		wantNone bool
	}{{
		name:   "an address is named by its own value, as an endpoint is",
		entity: ent(TypeNetworkAddress, []KeyValue{kv("network.address", "10.90.0.3")}, nil),
		want:   "10.90.0.3",
		// LabelName drops it: the label prints the identifying attributes right
		// after the name, so rendering both would read the address twice.
		wantLbl: "",
	}, {
		name: "a database is named by its technology and where it answers, not by its opaque key",
		entity: ent(TypeDatabase,
			[]KeyValue{kv("db.instance.id", "7269126174968421745")},
			[]KeyValue{kv("db.system.name", "postgresql"), kv("server.address", "10.0.0.5"), kv("server.port", "5432")}),
		want:    "postgresql@10.0.0.5:5432",
		wantLbl: "postgresql@10.0.0.5:5432",
	}, {
		name: "an IPv6 database address is bracketed so the port stays legible",
		entity: ent(TypeDatabase, []KeyValue{kv("db.instance.id", "x")},
			[]KeyValue{kv("db.system.name", "mysql"), kv("server.address", "2a01:db8::1"), kv("server.port", "3306")}),
		want:    "mysql@[2a01:db8::1]:3306",
		wantLbl: "mysql@[2a01:db8::1]:3306",
	}, {
		name: "a database with no address is named by its technology alone",
		entity: ent(TypeDatabase, []KeyValue{kv("db.instance.id", "x")},
			[]KeyValue{kv("db.system.name", "oracle")}),
		want:    "oracle",
		wantLbl: "oracle",
	}, {
		name:     "a database that reports no technology is not given an invented name",
		entity:   ent(TypeDatabase, []KeyValue{kv("db.instance.id", "x")}, nil),
		wantNone: true,
	}, {
		name:     "a segment declares that it has no name rather than rendering its opaque id",
		entity:   ent(TypeNetworkSegment, []KeyValue{kv("network.segment.id", "swarm:5f3a9c1d")}, nil),
		wantNone: true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DisplayName(c.entity)
			if c.wantNone {
				if got != "" {
					t.Fatalf("DisplayName = %q, want none", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("DisplayName = %q, want %q", got, c.want)
			}
			if lbl := LabelName(c.entity); lbl != c.wantLbl {
				t.Fatalf("LabelName = %q, want %q", lbl, c.wantLbl)
			}
		})
	}
}
