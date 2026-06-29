package model

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

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
	// KindArray is an ordered list of Values; KindKvlist an ordered list of
	// KeyValues. Together with the four scalars they make Value a faithful
	// mirror of the OpenTelemetry AnyValue (ADR 0004).
	KindArray
	KindKvlist
)

// Value is a typed attribute value mirroring OpenTelemetry's AnyValue: a string,
// int64, double, bool, an array of Values, or a kvlist of KeyValues (ADR 0004).
// The zero Value is invalid. Descriptive attributes carry the full AnyValue;
// identity values are kept scalar by contract (ADR 0018).
type Value struct {
	kind ValueKind
	s    string
	i    int64
	f    float64
	b    bool
	arr  []Value
	kvl  []KeyValue
}

// StringValue returns a string Value.
func StringValue(s string) Value { return Value{kind: KindString, s: s} }

// IntValue returns an int64 Value.
func IntValue(i int64) Value { return Value{kind: KindInt, i: i} }

// DoubleValue returns a float64 Value.
func DoubleValue(f float64) Value { return Value{kind: KindDouble, f: f} }

// BoolValue returns a bool Value.
func BoolValue(b bool) Value { return Value{kind: KindBool, b: b} }

// ArrayValue returns an array Value wrapping the given ordered elements.
func ArrayValue(vs []Value) Value { return Value{kind: KindArray, arr: vs} }

// KvlistValue returns a kvlist Value wrapping the given ordered key-values.
func KvlistValue(kvs []KeyValue) Value { return Value{kind: KindKvlist, kvl: kvs} }

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

// Array returns the elements of an array Value (nil for other kinds).
func (v Value) Array() []Value { return v.arr }

// Kvlist returns the entries of a kvlist Value (nil for other kinds).
func (v Value) Kvlist() []KeyValue { return v.kvl }

// String implements fmt.Stringer with a human-readable rendering.
func (v Value) String() string { return v.canonical() }

// Display renders the value as a plain string for human/consumer surfaces and
// attribute-equality matching — no type tag (unlike canonical, which is for
// identity hashing). Scalars render untyped (the string "1" and the integer 1
// render the same, intended for string-equality filters); arrays and kvlists
// render as compact JSON so a nested description stays legible to the consumer.
func (v Value) Display() string {
	switch v.kind {
	case KindInt:
		return strconv.FormatInt(v.i, 10)
	case KindDouble:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(v.b)
	case KindArray, KindKvlist:
		b, err := json.Marshal(v.toAny())
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return v.s
	}
}

// toAny converts the Value to a plain Go value suitable for JSON rendering.
// kvlists become maps (JSON marshals their keys sorted, which is deterministic).
func (v Value) toAny() any {
	switch v.kind {
	case KindString:
		return v.s
	case KindInt:
		return v.i
	case KindDouble:
		return v.f
	case KindBool:
		return v.b
	case KindArray:
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = e.toAny()
		}
		return out
	case KindKvlist:
		out := make(map[string]any, len(v.kvl))
		for _, kv := range v.kvl {
			out[kv.Key] = kv.Value.toAny()
		}
		return out
	default:
		return nil
	}
}

// canonical returns a deterministic, type-tagged string encoding used for
// identity hashing and attribute-change detection. The type tag ensures the
// string "1" and the integer 1 do not collide. Composite kinds are
// length-prefixed so nesting can never alias a different shape, and kvlist keys
// are sorted so map ordering does not affect the encoding.
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
	case KindArray:
		var sb strings.Builder
		sb.WriteString("a:")
		sb.WriteString(strconv.Itoa(len(v.arr)))
		sb.WriteByte(':')
		for _, e := range v.arr {
			c := e.canonical()
			sb.WriteString(strconv.Itoa(len(c)))
			sb.WriteByte(':')
			sb.WriteString(c)
		}
		return sb.String()
	case KindKvlist:
		entries := make([]KeyValue, len(v.kvl))
		copy(entries, v.kvl)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		var sb strings.Builder
		sb.WriteString("k:")
		sb.WriteString(strconv.Itoa(len(entries)))
		sb.WriteByte(':')
		for _, kv := range entries {
			sb.WriteString(strconv.Itoa(len(kv.Key)))
			sb.WriteByte(':')
			sb.WriteString(kv.Key)
			c := kv.Value.canonical()
			sb.WriteString(strconv.Itoa(len(c)))
			sb.WriteByte(':')
			sb.WriteString(c)
		}
		return sb.String()
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
	case KindArray:
		vals := make([]*toisev1.Value, len(v.arr))
		for i, e := range v.arr {
			vals[i] = e.toProto()
		}
		return &toisev1.Value{Value: &toisev1.Value_ArrayValue{ArrayValue: &toisev1.ArrayValue{Values: vals}}}
	case KindKvlist:
		return &toisev1.Value{Value: &toisev1.Value_KvlistValue{KvlistValue: &toisev1.KeyValueList{Values: kvsToProto(v.kvl)}}}
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
	case *toisev1.Value_ArrayValue:
		src := x.ArrayValue.GetValues()
		vs := make([]Value, len(src))
		for i, e := range src {
			vs[i] = valueFromProto(e)
		}
		return ArrayValue(vs)
	case *toisev1.Value_KvlistValue:
		return KvlistValue(kvsFromProto(x.KvlistValue.GetValues()))
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
