package protobufview

import (
	"errors"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ProtoJSON dispatches by full name, then accesses fields by number and kind.
// Validate that contract locally before any name-dispatched library call.
type wktField struct {
	name     protoreflect.Name
	kind     protoreflect.Kind
	target   protoreflect.FullName
	repeated bool
}

func wktFields(name protoreflect.FullName) ([]wktField, bool) {
	if name.Parent() != "google.protobuf" && name != "google.protobuf.Struct.FieldsEntry" {
		return nil, false
	}
	switch name {
	case "google.protobuf.Any":
		return []wktField{{"type_url", protoreflect.StringKind, "", false}, {"value", protoreflect.BytesKind, "", false}}, true
	case "google.protobuf.Timestamp", "google.protobuf.Duration":
		return []wktField{{"seconds", protoreflect.Int64Kind, "", false}, {"nanos", protoreflect.Int32Kind, "", false}}, true
	case "google.protobuf.Struct":
		return []wktField{{"fields", protoreflect.MessageKind, "google.protobuf.Struct.FieldsEntry", true}}, true
	case "google.protobuf.Struct.FieldsEntry":
		return []wktField{{"key", protoreflect.StringKind, "", false}, {"value", protoreflect.MessageKind, "google.protobuf.Value", false}}, true
	case "google.protobuf.ListValue":
		return []wktField{{"values", protoreflect.MessageKind, "google.protobuf.Value", true}}, true
	case "google.protobuf.Value":
		return []wktField{{"null_value", protoreflect.EnumKind, "google.protobuf.NullValue", false}, {"number_value", protoreflect.DoubleKind, "", false}, {"string_value", protoreflect.StringKind, "", false}, {"bool_value", protoreflect.BoolKind, "", false}, {"struct_value", protoreflect.MessageKind, "google.protobuf.Struct", false}, {"list_value", protoreflect.MessageKind, "google.protobuf.ListValue", false}}, true
	case "google.protobuf.FieldMask":
		return []wktField{{"paths", protoreflect.StringKind, "", true}}, true
	case "google.protobuf.Empty":
		return nil, true
	}
	kinds := map[protoreflect.FullName]protoreflect.Kind{
		"google.protobuf.BoolValue": protoreflect.BoolKind, "google.protobuf.Int32Value": protoreflect.Int32Kind,
		"google.protobuf.Int64Value": protoreflect.Int64Kind, "google.protobuf.UInt32Value": protoreflect.Uint32Kind,
		"google.protobuf.UInt64Value": protoreflect.Uint64Kind, "google.protobuf.FloatValue": protoreflect.FloatKind,
		"google.protobuf.DoubleValue": protoreflect.DoubleKind, "google.protobuf.StringValue": protoreflect.StringKind,
		"google.protobuf.BytesValue": protoreflect.BytesKind,
	}
	if k, ok := kinds[name]; ok {
		return []wktField{{"value", k, "", false}}, true
	}
	return nil, false
}

func validateWKT(m protoreflect.MessageDescriptor) error {
	expected, known := wktFields(m.FullName())
	if !known {
		return nil
	}
	invalid := errors.New("descriptor: invalid well-known message shape")
	if m.Syntax() != protoreflect.Proto3 || m.Fields().Len() != len(expected) || m.Extensions().Len() != 0 || m.ExtensionRanges().Len() != 0 {
		return invalid
	}
	value := m.FullName() == "google.protobuf.Value"
	if value {
		if m.Oneofs().Len() != 1 || m.Oneofs().Get(0).Name() != "kind" || m.Oneofs().Get(0).IsSynthetic() {
			return invalid
		}
	} else if m.Oneofs().Len() != 0 {
		return invalid
	}
	if m.IsMapEntry() != (m.FullName() == "google.protobuf.Struct.FieldsEntry") {
		return invalid
	}
	for i, w := range expected {
		f := m.Fields().ByNumber(protoreflect.FieldNumber(i + 1))
		if f == nil || f.Name() != w.name || f.Kind() != w.kind || f.HasDefault() || f.HasOptionalKeyword() || f.Cardinality() == protoreflect.Required || (f.Cardinality() == protoreflect.Repeated) != w.repeated {
			return invalid
		}
		if (f.ContainingOneof() != nil) != value || f.JSONName() != camel(string(w.name)) {
			return invalid
		}
		if f.Kind() == protoreflect.MessageKind && f.Message().FullName() != w.target {
			return invalid
		}
		if f.Kind() == protoreflect.EnumKind && f.Enum().FullName() != w.target {
			return invalid
		}
		if f.IsMap() != (m.FullName() == "google.protobuf.Struct") {
			return invalid
		}
	}
	return nil
}

// These ASCII transforms match the pinned ProtoJSON FieldMask rules. Invalid
// identifiers and non-reversible conversions are rejected before size accounting.
func camel(s string) string {
	var b strings.Builder
	upper := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		upper = false
		b.WriteByte(c)
	}
	return b.String()
}
func snake(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('_')
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

func special(name protoreflect.FullName) bool {
	_, known := wktFields(name)
	return known && name != "google.protobuf.Empty" && name != "google.protobuf.Struct.FieldsEntry"
}
