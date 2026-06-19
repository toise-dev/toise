package model

import (
	"strconv"

	toisev1 "github.com/toise-dev/toise/proto/toise/v1"
)

// ValueKind is the type tag of a Value.
type ValueKind uint8

// Value kinds. KindInvalid is the zero value (an unset Value).
const (
	KindInvalid ValueKind = iota
	KindString
	KindInt
	KindDouble
	KindBool
)

// Value is a typed attribute value: a string, int64, double, or bool. It is a
// deliberate subset of OpenTelemetry's AnyValue (see ADR 0004). The zero Value
// is invalid.
type Value struct {
	kind ValueKind
	s    string
	i    int64
	f    float64
	b    bool
}

// StringValue returns a string Value.
func StringValue(s string) Value { return Value{kind: KindString, s: s} }

// IntValue returns an int64 Value.
func IntValue(i int64) Value { return Value{kind: KindInt, i: i} }

// DoubleValue returns a float64 Value.
func DoubleValue(f float64) Value { return Value{kind: KindDouble, f: f} }

// BoolValue returns a bool Value.
func BoolValue(b bool) Value { return Value{kind: KindBool, b: b} }

// Kind returns the Value's type tag.
func (v Value) Kind() ValueKind { return v.kind }

// IsValid reports whether the Value is set.
func (v Value) IsValid() bool { return v.kind != KindInvalid }

// Str returns the string content (zero value if the kind is not KindString).
func (v Value) Str() string { return v.s }

// Int returns the int64 content.
func (v Value) Int() int64 { return v.i }

// Double returns the float64 content.
func (v Value) Double() float64 { return v.f }

// Bool returns the bool content.
func (v Value) Bool() bool { return v.b }

// String implements fmt.Stringer with a human-readable rendering.
func (v Value) String() string { return v.canonical() }

// Display renders the value as a plain, untyped string for human/consumer
// surfaces and attribute-equality matching — no type tag (unlike canonical,
// which is for identity hashing). A string "1" and the integer 1 render the
// same here, which is the intended behavior for a string-equality filter.
func (v Value) Display() string {
	switch v.kind {
	case KindInt:
		return strconv.FormatInt(v.i, 10)
	case KindDouble:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(v.b)
	default:
		return v.s
	}
}

// canonical returns a deterministic, type-tagged string encoding used for
// identity hashing. The type tag ensures the string "1" and the integer 1 do
// not collide.
func (v Value) canonical() string {
	switch v.kind {
	case KindString:
		return "s:" + v.s
	case KindInt:
		return "i:" + strconv.FormatInt(v.i, 10)
	case KindDouble:
		return "d:" + strconv.FormatFloat(v.f, 'g', -1, 64)
	case KindBool:
		return "b:" + strconv.FormatBool(v.b)
	default:
		return "n:"
	}
}

// KeyValue is a single attribute: a key paired with a typed Value.
type KeyValue struct {
	Key   string
	Value Value
}

func (v Value) toProto() *toisev1.Value {
	switch v.kind {
	case KindString:
		return &toisev1.Value{Value: &toisev1.Value_StringValue{StringValue: v.s}}
	case KindInt:
		return &toisev1.Value{Value: &toisev1.Value_IntValue{IntValue: v.i}}
	case KindDouble:
		return &toisev1.Value{Value: &toisev1.Value_DoubleValue{DoubleValue: v.f}}
	case KindBool:
		return &toisev1.Value{Value: &toisev1.Value_BoolValue{BoolValue: v.b}}
	default:
		return &toisev1.Value{}
	}
}

func valueFromProto(p *toisev1.Value) Value {
	if p == nil {
		return Value{}
	}
	switch x := p.GetValue().(type) {
	case *toisev1.Value_StringValue:
		return StringValue(x.StringValue)
	case *toisev1.Value_IntValue:
		return IntValue(x.IntValue)
	case *toisev1.Value_DoubleValue:
		return DoubleValue(x.DoubleValue)
	case *toisev1.Value_BoolValue:
		return BoolValue(x.BoolValue)
	default:
		return Value{}
	}
}

func kvsToProto(kvs []KeyValue) []*toisev1.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]*toisev1.KeyValue, len(kvs))
	for i, kv := range kvs {
		out[i] = &toisev1.KeyValue{Key: kv.Key, Value: kv.Value.toProto()}
	}
	return out
}

func kvsFromProto(pks []*toisev1.KeyValue) []KeyValue {
	if len(pks) == 0 {
		return nil
	}
	out := make([]KeyValue, len(pks))
	for i, pk := range pks {
		out[i] = KeyValue{Key: pk.GetKey(), Value: valueFromProto(pk.GetValue())}
	}
	return out
}
