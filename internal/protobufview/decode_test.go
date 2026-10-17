package protobufview

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

func field(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Type: kind.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()}
}
func messageFile(syntax string, ms ...*descriptorpb.DescriptorProto) *descriptorpb.FileDescriptorProto {
	return &descriptorpb.FileDescriptorProto{Name: proto.String("fixture.proto"), Package: proto.String("example"), Syntax: proto.String(syntax), MessageType: ms}
}
func descriptorBytes(t *testing.T, files ...*descriptorpb.FileDescriptorProto) []byte {
	t.Helper()
	data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func prepared(t *testing.T, name string, files ...*descriptorpb.FileDescriptorProto) *decoder {
	t.Helper()
	d, err := prepare(context.Background(), descriptorBytes(t, files...), name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func wire(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func valueJSON(t *testing.T, v omap.Value) string {
	t.Helper()
	var b bytes.Buffer
	if err := jsonfmt.EncodeValue(&b, v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func checkJSON(t *testing.T, d *decoder, data []byte, want string) {
	t.Helper()
	v, err := d.decode(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if got := valueJSON(t, v); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
func wkts() []*descriptorpb.FileDescriptorProto {
	var files []*descriptorpb.FileDescriptorProto
	for _, f := range []protoreflect.FileDescriptor{anypb.File_google_protobuf_any_proto, durationpb.File_google_protobuf_duration_proto, emptypb.File_google_protobuf_empty_proto, fieldmaskpb.File_google_protobuf_field_mask_proto, structpb.File_google_protobuf_struct_proto, timestamppb.File_google_protobuf_timestamp_proto, wrapperspb.File_google_protobuf_wrappers_proto} {
		files = append(files, protodesc.ToFileDescriptorProto(f))
	}
	return files
}

func TestMappingsAndPresence(t *testing.T) {
	fields := []*descriptorpb.FieldDescriptorProto{
		field("z_text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("wide", 2, descriptorpb.FieldDescriptorProto_TYPE_INT64), field("unsigned", 3, descriptorpb.FieldDescriptorProto_TYPE_UINT64), field("blob", 4, descriptorpb.FieldDescriptorProto_TYPE_BYTES), field("mode", 5, descriptorpb.FieldDescriptorProto_TYPE_ENUM), field("special", 6, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE), field("optional_count", 7, descriptorpb.FieldDescriptorProto_TYPE_INT32), field("choice", 8, descriptorpb.FieldDescriptorProto_TYPE_BOOL), field("implicit", 9, descriptorpb.FieldDescriptorProto_TYPE_INT32),
	}
	fields[4].TypeName = proto.String(".example.Mode")
	fields[6].OneofIndex = proto.Int32(1)
	fields[6].Proto3Optional = proto.Bool(true)
	fields[7].OneofIndex = proto.Int32(0)
	m := &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: fields, OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("chosen")}, {Name: proto.String("_optional_count")}}}
	f := messageFile("proto3", m)
	f.EnumType = []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Mode"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("ZERO"), Number: proto.Int32(0)}, {Name: proto.String("READY"), Number: proto.Int32(1)}}}}
	d := prepared(t, "example.Event", f)
	msg := d.message.New()
	fs := msg.Descriptor().Fields()
	msg.Set(fs.ByNumber(1), protoreflect.ValueOfString("hello"))
	msg.Set(fs.ByNumber(2), protoreflect.ValueOfInt64(math.MinInt64))
	msg.Set(fs.ByNumber(3), protoreflect.ValueOfUint64(math.MaxUint64))
	msg.Set(fs.ByNumber(4), protoreflect.ValueOfBytes([]byte{0, 255}))
	msg.Set(fs.ByNumber(5), protoreflect.ValueOfEnum(1))
	msg.Set(fs.ByNumber(6), protoreflect.ValueOfFloat64(math.Inf(-1)))
	msg.Set(fs.ByNumber(7), protoreflect.ValueOfInt32(0))
	msg.Set(fs.ByNumber(8), protoreflect.ValueOfBool(false))
	msg.SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 100, protowire.VarintType), 12))
	checkJSON(t, d, wire(t, msg.Interface()), `{"blob":"AP8=","choice":false,"mode":"READY","optionalCount":0,"special":"-Infinity","unsigned":"18446744073709551615","wide":"-9223372036854775808","zText":"hello"}`)
	msg.Set(fs.ByNumber(5), protoreflect.ValueOfEnum(99))
	v, err := d.decode(context.Background(), wire(t, msg.Interface()))
	if err != nil {
		t.Fatal(err)
	}
	mode, _ := v.Map.Get("mode")
	if mode.Kind != omap.KindNum || mode.Num != "99" {
		t.Fatalf("unknown enum: %+v", mode)
	}
	checkJSON(t, d, nil, `{}`)
	if _, err := d.decode(context.Background(), []byte{0xff}); err == nil {
		t.Fatal("malformed wire accepted")
	}
}

func TestAllWellKnownMappings(t *testing.T) {
	tests := []struct {
		m    proto.Message
		want string
	}{
		{wrapperspb.Bool(false), `false`}, {wrapperspb.Int32(-4), `-4`}, {wrapperspb.Int64(0), `"0"`}, {wrapperspb.UInt32(4), `4`}, {wrapperspb.UInt64(math.MaxUint64), `"18446744073709551615"`},
		{wrapperspb.Float(float32(math.NaN())), `"NaN"`}, {wrapperspb.Double(math.Inf(1)), `"Infinity"`}, {wrapperspb.String("x\n\u0000\\\"é<>&"), `"x\n\u0000\\\"é\u003c\u003e\u0026"`}, {wrapperspb.Bytes([]byte{255}), `"/w=="`},
		{&timestamppb.Timestamp{Seconds: -62135596800, Nanos: 123000000}, `"0001-01-01T00:00:00.123Z"`},
		{&durationpb.Duration{Seconds: -12, Nanos: -123456000}, `"-12.123456s"`},
		{&fieldmaskpb.FieldMask{Paths: []string{"foo_bar", "nested.some_field"}}, `"fooBar,nested.someField"`},
		{&fieldmaskpb.FieldMask{}, `""`}, {&emptypb.Empty{}, `{}`},
		{structpb.NewNullValue(), `null`}, {structpb.NewStringValue("s"), `"s"`}, {structpb.NewNumberValue(2), `2`}, {structpb.NewBoolValue(true), `true`},
		{&structpb.ListValue{Values: []*structpb.Value{structpb.NewNullValue(), structpb.NewStringValue("a")}}, `[null,"a"]`},
		{&structpb.Struct{Fields: map[string]*structpb.Value{"z": structpb.NewBoolValue(true), "a": structpb.NewListValue(&structpb.ListValue{})}}, `{"a":[],"z":true}`},
		{&anypb.Any{}, `{}`},
	}
	anyDecoder := prepared(t, "google.protobuf.Any", wkts()...)
	for _, tt := range tests {
		t.Run(string(tt.m.ProtoReflect().Descriptor().Name())+tt.want, func(t *testing.T) {
			d := prepared(t, string(tt.m.ProtoReflect().Descriptor().FullName()), wkts()...)
			checkJSON(t, d, wire(t, tt.m), tt.want)
			wrapped := &anypb.Any{TypeUrl: "local/" + string(tt.m.ProtoReflect().Descriptor().FullName()), Value: wire(t, tt.m)}
			expanded, err := anyDecoder.decode(context.Background(), wire(t, wrapped))
			if err != nil {
				t.Fatal(err)
			}
			inner, hasValue := expanded.Map.Get("value")
			if tt.m.ProtoReflect().Descriptor().FullName() == "google.protobuf.Empty" {
				if hasValue {
					t.Fatal("Empty must not have a special Any value wrapper")
				}
			} else if !hasValue || valueJSON(t, inner) != tt.want {
				t.Fatalf("Any mapping: %s", valueJSON(t, expanded))
			}
			// Differential certificate and shape accounting against the real serializer.
			m := d.message.New()
			if err := (proto.UnmarshalOptions{Resolver: d.types}).Unmarshal(wire(t, tt.m), m.Interface()); err != nil {
				t.Fatal(err)
			}
			p := newPreflight(context.Background(), d)
			if err := p.decodedMessage(m, 0); err != nil {
				t.Fatal(err)
			}
			if err := p.viewMessage(m, 0, ""); err != nil {
				t.Fatal(err)
			}
			raw, err := (protojson.MarshalOptions{Resolver: d.types}).Marshal(m.Interface())
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) > p.certificate {
				t.Fatalf("size %d > certificate %d", len(raw), p.certificate)
			}
			v, err := jsonfmt.DecodeValue(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			b := budget{stage: "actual"}
			if _, err := sortView(context.Background(), v, 0, &b); err != nil {
				t.Fatal(err)
			}
			if b.nodes != p.view.nodes || b.text != p.view.text {
				t.Fatalf("predicted %+v actual %+v", p.view, b)
			}
		})
	}
}

func TestInvalidWellKnownValues(t *testing.T) {
	cases := []proto.Message{&structpb.Value{}, structpb.NewNumberValue(math.NaN()), &timestamppb.Timestamp{Seconds: 253402300800}, &timestamppb.Timestamp{Nanos: -1}, &durationpb.Duration{Seconds: 1, Nanos: -1}, &durationpb.Duration{Nanos: 1000000000}, &durationpb.Duration{Seconds: 315576000001}, &fieldmaskpb.FieldMask{Paths: []string{"bad__path"}}, &fieldmaskpb.FieldMask{Paths: []string{"Bad"}}, &fieldmaskpb.FieldMask{Paths: []string{"a..b"}}, &anypb.Any{Value: []byte{1}}, &anypb.Any{TypeUrl: "not-local/Missing"}}
	for _, m := range cases {
		d := prepared(t, string(m.ProtoReflect().Descriptor().FullName()), wkts()...)
		if _, err := d.decode(context.Background(), wire(t, m)); err == nil {
			t.Fatalf("accepted invalid %T", m)
		}
	}
}

func TestAnyExpansionAndRequiredExtensions(t *testing.T) {
	required := field("must", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	required.Label = descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum()
	f := messageFile("proto2", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{required}, ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{{Start: proto.Int32(100), End: proto.Int32(200)}}})
	x := field("extra", 100, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	x.Extendee = proto.String(".example.Event")
	f.Extension = []*descriptorpb.FieldDescriptorProto{x}
	files := append(wkts(), f)
	d := prepared(t, "google.protobuf.Any", files...)
	body := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "ok")
	body = protowire.AppendString(protowire.AppendTag(body, 100, protowire.BytesType), "extension")
	body = protowire.AppendVarint(protowire.AppendTag(body, 101, protowire.VarintType), 1)
	a := &anypb.Any{TypeUrl: "https://never-fetched.invalid/example.Event", Value: body}
	checkJSON(t, d, wire(t, a), `{"@type":"https://never-fetched.invalid/example.Event","[example.extra]":"extension","must":"ok"}`)
	nested := &anypb.Any{TypeUrl: "prefix/google.protobuf.Any", Value: wire(t, a)}
	v, err := d.decode(context.Background(), wire(t, nested))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(valueJSON(t, v), `"value":{"@type"`) {
		t.Fatal("nested Any did not expand")
	}
	for _, target := range []proto.Message{wrapperspb.String("scalar"), &structpb.ListValue{Values: []*structpb.Value{structpb.NewBoolValue(true)}}, &structpb.Struct{Fields: map[string]*structpb.Value{"a": structpb.NewNullValue()}}, &emptypb.Empty{}} {
		url := "local/" + string(target.ProtoReflect().Descriptor().FullName())
		a := &anypb.Any{TypeUrl: url, Value: wire(t, target)}
		result, err := d.decode(context.Background(), wire(t, a))
		if err != nil {
			t.Fatal(err)
		}
		if target.ProtoReflect().Descriptor().FullName() == "google.protobuf.Empty" {
			if result.Map.Has("value") {
				t.Fatal("Empty received special wrapper")
			}
		} else if !result.Map.Has("value") {
			t.Fatal("special Any lost value")
		}
	}
	a.Value = nil
	if _, err := d.decode(context.Background(), wire(t, a)); err == nil {
		t.Fatal("missing required in Any accepted")
	}
	nested.Value = wire(t, a)
	if _, err := d.decode(context.Background(), wire(t, nested)); err == nil {
		t.Fatal("nested missing required accepted")
	}
	event := prepared(t, "example.Event", files...)
	if _, err := event.decode(context.Background(), nil); err == nil {
		t.Fatal("empty required type accepted")
	}
	a.Value = []byte{0xff}
	if _, err := d.decode(context.Background(), wire(t, a)); err == nil {
		t.Fatal("bad Any wire accepted")
	}
}

func TestRecursiveObjectOrdering(t *testing.T) {
	d := prepared(t, "google.protobuf.Struct", wkts()...)
	m, _ := structpb.NewStruct(map[string]any{"z": map[string]any{"z": true, "a": false}, "a": []any{map[string]any{"9": 9, "10": 10}, 2}})
	checkJSON(t, d, wire(t, m), `{"a":[{"10":10,"9":9},2],"z":{"a":false,"z":true}}`)
}

func TestScalarCertificateDifferential(t *testing.T) {
	for _, n := range []float64{0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, math.MaxFloat64, 1e20, 1e21, 1e-6, 1e-7, -1.2345678901234567e20} {
		for _, m := range []proto.Message{wrapperspb.Double(n), wrapperspb.Float(float32(n))} {
			d := prepared(t, string(m.ProtoReflect().Descriptor().FullName()), wkts()...)
			if _, err := d.decode(context.Background(), wire(t, m)); err != nil {
				t.Fatalf("%T %g: %v", m, n, err)
			}
		}
	}
}

func TestProjectedNumberPreservedAsString(t *testing.T) {
	d := prepared(t, "google.protobuf.Int64Value", wkts()...)
	v, err := d.decode(context.Background(), wire(t, wrapperspb.Int64(9007199254740993)))
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != omap.KindStr || v.Str != "9007199254740993" {
		t.Fatal(v)
	}
	var result any
	if err := json.Unmarshal([]byte(valueJSON(t, v)), &result); err != nil {
		t.Fatal(err)
	}
}
