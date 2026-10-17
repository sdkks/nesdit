package protobufview

import (
	"context"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestOrdinaryMapsAndNestedPresence(t *testing.T) {
	entry := &descriptorpb.DescriptorProto{Name: proto.String("ItemsEntry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_INT64), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}}
	items := field("items", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	items.TypeName = proto.String(".example.Event.ItemsEntry")
	items.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	child := field("child", 2, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	child.TypeName = proto.String(".example.Child")
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{items, child}, NestedType: []*descriptorpb.DescriptorProto{entry}}, &descriptorpb.DescriptorProto{Name: proto.String("Child")})
	d := prepared(t, "example.Event", f)
	m := d.message.New()
	fs := m.Descriptor().Fields()
	values := m.Mutable(fs.ByNumber(1)).Map()
	values.Set(protoreflect.ValueOfInt64(2).MapKey(), protoreflect.ValueOfString("second"))
	values.Set(protoreflect.ValueOfInt64(10).MapKey(), protoreflect.ValueOfString("tenth"))
	m.Mutable(fs.ByNumber(2))
	checkJSON(t, d, wire(t, m.Interface()), `{"child":{},"items":{"10":"tenth","2":"second"}}`)
}

func TestProto2GroupsDefaultsAndNestedRequired(t *testing.T) {
	required := field("required_text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	required.Label = descriptorpb.FieldDescriptorProto_LABEL_REQUIRED.Enum()
	group := field("details", 1, descriptorpb.FieldDescriptorProto_TYPE_GROUP)
	group.TypeName = proto.String(".example.Event.Details")
	defaulted := field("count", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32)
	defaulted.DefaultValue = proto.String("7")
	f := messageFile("proto2", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{group, defaulted}, NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("Details"), Field: []*descriptorpb.FieldDescriptorProto{required}}}})
	d := prepared(t, "example.Event", f)
	checkJSON(t, d, nil, `{}`)
	checkJSON(t, d, []byte{16, 7}, `{"count":7}`)
	if _, err := d.decode(context.Background(), []byte{11, 12}); err == nil {
		t.Fatal("nested required field accepted")
	}
	checkJSON(t, d, []byte{11, 10, 1, 'x', 12}, `{"details":{"requiredText":"x"}}`)
	// Proto2 allows invalid UTF-8 on the wire, but a JSON view must reject it.
	if _, err := d.decode(context.Background(), []byte{11, 10, 1, 255, 12}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}

func TestDecodedScalarsDoNotChargeMappedText(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value protoreflect.Value
		text  int
	}{
		{"google.protobuf.Int64Value", protoreflect.ValueOfInt64(-9223372036854775808), 20},
		{"google.protobuf.DoubleValue", protoreflect.ValueOfFloat64(math.Inf(1)), 8},
	} {
		d := prepared(t, tt.name, wkts()...)
		m := d.message.New()
		m.Set(m.Descriptor().Fields().ByNumber(1), tt.value)
		p := newPreflight(context.Background(), d)
		if err := p.decodedMessage(m, 0); err != nil {
			t.Fatal(err)
		}
		if err := p.viewMessage(m, 0, ""); err != nil {
			t.Fatal(err)
		}
		if p.decoded.text != len("value") || p.view.text != tt.text {
			t.Fatalf("%s reflection text=%d view text=%d", tt.name, p.decoded.text, p.view.text)
		}
	}
}

func TestCertificateStringPreflightEquality(t *testing.T) {
	p := newPreflight(context.Background(), nil)
	exact := strings.Repeat("a", serializedLimit-2)
	if err := p.string(exact, 0); err != nil {
		t.Fatal(err)
	}
	if p.certificate != serializedLimit || p.view.text != serializedLimit-2 || p.view.nodes != 1 {
		t.Fatalf("wrong scalar preflight %+v", p)
	}
	p = newPreflight(context.Background(), nil)
	if err := p.string(exact+"a", 0); err == nil {
		t.Fatal("one-over actual scalar certificate accepted")
	}
}
