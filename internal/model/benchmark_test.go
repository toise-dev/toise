package model

import "testing"

func benchEntity() Entity {
	return Entity{
		ID:   NewEntityID(),
		Type: TypeHost,
		Identity: []KeyValue{
			{Key: "host.id", Value: StringValue("0123456789abcdef")},
			{Key: "host.name", Value: StringValue("web-server-1.example.com")},
		},
		Attributes: []KeyValue{
			{Key: "os", Value: StringValue("linux")},
			{Key: "cpu.count", Value: IntValue(8)},
		},
		SchemaURL: "https://schemas.toise.dev/host/1.0",
	}
}

func BenchmarkIdentityHash(b *testing.B) {
	e := benchEntity()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.IdentityHash()
	}
}

func BenchmarkEntityToProto(b *testing.B) {
	e := benchEntity()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = e.ToProto()
	}
}
