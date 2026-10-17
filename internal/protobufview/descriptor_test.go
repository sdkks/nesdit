package protobufview

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorFailures(t *testing.T) {
	base := func() *descriptorpb.FileDescriptorProto {
		return messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{field("a", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("b", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}})
	}
	cases := []struct {
		name string
		edit func(*descriptorpb.FileDescriptorProto)
	}{
		{"ordinary import", func(f *descriptorpb.FileDescriptorProto) { f.Dependency = []string{"missing.proto"} }},
		{"option import", func(f *descriptorpb.FileDescriptorProto) { f.OptionDependency = []string{"missing.proto"} }},
		{"self import", func(f *descriptorpb.FileDescriptorProto) { f.Dependency = []string{f.GetName()} }},
		{"collision", func(f *descriptorpb.FileDescriptorProto) { f.MessageType[0].Field[1].JsonName = proto.String("a") }},
		{"reserved Any key", func(f *descriptorpb.FileDescriptorProto) { f.MessageType[0].Field[0].JsonName = proto.String("@type") }},
		{"nul key", func(f *descriptorpb.FileDescriptorProto) { f.MessageType[0].Field[0].JsonName = proto.String("a\x00b") }},
		{"invalid UTF8 key", func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[0].Field[0].JsonName = proto.String(string([]byte{255}))
		}},
		{"syntax", func(f *descriptorpb.FileDescriptorProto) { f.Syntax = proto.String("future") }},
		{"unknown edition", func(f *descriptorpb.FileDescriptorProto) {
			f.Syntax = proto.String("editions")
			f.Edition = descriptorpb.Edition_EDITION_UNSTABLE.Enum()
			f.Name = proto.String("cmd/protoc-gen-go/testdata/bypass.proto")
		}},
		{"MessageSet", func(f *descriptorpb.FileDescriptorProto) {
			f.Syntax = proto.String("proto2")
			f.MessageType[0].Field = nil
			f.MessageType[0].Options = &descriptorpb.MessageOptions{MessageSetWireFormat: proto.Bool(true)}
		}},
		{"missing type", func(f *descriptorpb.FileDescriptorProto) {
			f.MessageType[0].Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
			f.MessageType[0].Field[0].TypeName = proto.String(".example.Missing")
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := base()
			tt.edit(f)
			data, err := proto.Marshal(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{f}})
			// Bypass only the test fixture encoder's UTF-8 rejection by writing
			// the same field as raw wire; production preparation must reject it.
			if err != nil && tt.name == "invalid UTF8 key" {
				f.MessageType[0].Field[0].JsonName = nil
				f.MessageType[0].Field[0].ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, 10, protowire.BytesType), string([]byte{255})))
				data = descriptorBytes(t, f)
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepare(context.Background(), data, "example.Event"); err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
	f := base()
	if _, err := prepare(context.Background(), descriptorBytes(t, f, f), "example.Event"); err == nil {
		t.Fatal("duplicate files accepted")
	}
	g := proto.Clone(f).(*descriptorpb.FileDescriptorProto)
	g.Name = proto.String("other.proto")
	if _, err := prepare(context.Background(), descriptorBytes(t, f, g), "example.Event"); err == nil {
		t.Fatal("conflicting definitions accepted")
	}
	for _, name := range []string{".example.Event", "url/example.Event", "example.Missing", "", "example.Event "} {
		if _, err := prepare(context.Background(), descriptorBytes(t, f), name); err == nil {
			t.Fatalf("invalid selected name %q", name)
		}
	}
	if _, err := prepare(context.Background(), []byte{255}, "example.Event"); err == nil {
		t.Fatal("malformed descriptor accepted")
	}
}

func TestExtensionCollisions(t *testing.T) {
	makeFile := func() *descriptorpb.FileDescriptorProto {
		f := messageFile("proto2", &descriptorpb.DescriptorProto{Name: proto.String("Event"), ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{{Start: proto.Int32(100), End: proto.Int32(200)}}})
		a := field("a", 100, descriptorpb.FieldDescriptorProto_TYPE_INT32)
		a.Extendee = proto.String(".example.Event")
		b := field("b", 100, descriptorpb.FieldDescriptorProto_TYPE_INT32)
		b.Extendee = proto.String(".example.Event")
		f.Extension = []*descriptorpb.FieldDescriptorProto{a, b}
		return f
	}
	if _, err := prepare(context.Background(), descriptorBytes(t, makeFile()), "example.Event"); err == nil {
		t.Fatal("duplicate extension number accepted")
	}
	f := makeFile()
	f.Extension = f.Extension[:1]
	ordinary := field("collision", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)
	ordinary.JsonName = proto.String("[example.a]")
	f.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{ordinary}
	if _, err := prepare(context.Background(), descriptorBytes(t, f), "example.Event"); err == nil {
		t.Fatal("extension JSON name collision accepted")
	}
}

func TestFakeWellKnownShapes(t *testing.T) {
	for _, name := range []string{"Any", "Timestamp", "Duration", "BoolValue", "Int32Value", "Int64Value", "UInt32Value", "UInt64Value", "FloatValue", "DoubleValue", "StringValue", "BytesValue", "Struct", "ListValue", "Value", "FieldMask", "Empty"} {
		t.Run(name, func(t *testing.T) {
			fs := wkts()
			var target *descriptorpb.DescriptorProto
			for _, f := range fs {
				for _, m := range f.MessageType {
					if m.GetName() == name {
						target = m
					}
				}
			}
			if target == nil {
				t.Fatal("fixture missing")
			}
			if len(target.Field) > 0 {
				target.Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()
				target.Field[0].TypeName = nil
			} else {
				target.Field = []*descriptorpb.FieldDescriptorProto{field("extra", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}
			}
			// BoolValue needs a distinct wrong kind.
			if name == "BoolValue" {
				target.Field[0].Type = descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
			}
			if _, err := prepare(context.Background(), descriptorBytes(t, fs...), "google.protobuf."+name); err == nil {
				t.Fatal("fake WKT accepted")
			}
		})
	}
	fs := wkts()
	for _, f := range fs {
		for _, e := range f.EnumType {
			if e.GetName() == "NullValue" {
				e.Value[0].Name = proto.String("FAKE")
			}
		}
	}
	if _, err := prepare(context.Background(), descriptorBytes(t, fs...), "google.protobuf.Value"); err == nil {
		t.Fatal("fake NullValue accepted")
	}
	// A generated/global WKT is never an implicit local import or Any target.
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event")})
	f.Dependency = []string{"google/protobuf/any.proto"}
	if _, err := prepare(context.Background(), descriptorBytes(t, f), "example.Event"); err == nil {
		t.Fatal("ambient WKT import used")
	}
}

func TestOptionImportsAndEditions(t *testing.T) {
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event")})
	f.OptionDependency = []string{"option.proto"}
	option := &descriptorpb.FileDescriptorProto{Name: proto.String("option.proto"), Syntax: proto.String("proto3")}
	prepared(t, "example.Event", f, option)
	for _, edition := range []descriptorpb.Edition{descriptorpb.Edition_EDITION_2023, descriptorpb.Edition_EDITION_2024} {
		f := messageFile("editions", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{field("count", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32)}})
		f.Edition = edition.Enum()
		d := prepared(t, "example.Event", f)
		checkJSON(t, d, []byte{8, 0}, `{"count":0}`)
	}
}

func TestDescriptorBoundaries(t *testing.T) {
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event")})
	base := descriptorBytes(t, f)
	// Unknown bytes are still subject to the descriptor raw cap.
	padding := descriptorLimit - len(base) - 4
	for protowire.SizeTag(100)+protowire.SizeBytes(padding)+len(base) != descriptorLimit {
		padding--
	}
	exact := protowire.AppendBytes(protowire.AppendTag(append([]byte(nil), base...), 100, protowire.BytesType), make([]byte, padding))
	if len(exact) != descriptorLimit {
		t.Fatal(len(exact))
	}
	if _, err := prepare(context.Background(), exact, "example.Event"); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), append(exact, 0), "example.Event"); err == nil {
		t.Fatal("descriptor byte cap not enforced")
	}
	files := []*descriptorpb.FileDescriptorProto{f}
	for i := 1; i < 256; i++ {
		files = append(files, &descriptorpb.FileDescriptorProto{Name: proto.String(fmt.Sprintf("f%d.proto", i))})
	}
	prepared(t, "example.Event", files...)
	files = append(files, &descriptorpb.FileDescriptorProto{Name: proto.String("over.proto")})
	if _, err := prepare(context.Background(), descriptorBytes(t, files...), "example.Event"); err == nil {
		t.Fatal("file cap not enforced")
	}
	for _, n := range []int{9998, 9999} {
		f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event")})
		for i := 0; i < n; i++ {
			f.MessageType[0].Field = append(f.MessageType[0].Field, field(fmt.Sprintf("f_%d", i), int32(i+1), descriptorpb.FieldDescriptorProto_TYPE_INT32))
		}
		_, err := prepare(context.Background(), descriptorBytes(t, f), "example.Event")
		if (err == nil) != (n == 9998) {
			t.Fatalf("%d fields: %v", n, err)
		}
	}
	for _, depth := range []int{100, 101} {
		root := &descriptorpb.DescriptorProto{Name: proto.String("Event")}
		current := root
		// Set(0), file(1), root message(2), then nested messages.
		for i := 2; i < depth; i++ {
			child := &descriptorpb.DescriptorProto{Name: proto.String("Child")}
			current.NestedType = []*descriptorpb.DescriptorProto{child}
			current = child
		}
		_, err := prepare(context.Background(), descriptorBytes(t, messageFile("proto3", root)), "example.Event")
		if (err == nil) != (depth == 100) {
			t.Fatalf("descriptor depth %d: %v", depth, err)
		}
	}
	for _, depth := range []int{100, 101} {
		files := []*descriptorpb.FileDescriptorProto{messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Event")})}
		files[0].Dependency = []string{"dep1.proto"}
		for i := 1; i <= depth; i++ {
			f := &descriptorpb.FileDescriptorProto{Name: proto.String(fmt.Sprintf("dep%d.proto", i))}
			if i < depth {
				f.Dependency = []string{fmt.Sprintf("dep%d.proto", i+1)}
			}
			files = append(files, f)
		}
		_, err := prepare(context.Background(), descriptorBytes(t, files...), "example.Event")
		if (err == nil) != (depth == 100) {
			t.Fatalf("import depth %d: %v", depth, err)
		}
	}
}

func TestDeclarationAccountingIncludesSyntheticAndOptionsDepth(t *testing.T) {
	root := &descriptorpb.DescriptorProto{Name: proto.String("Root"), OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("choice")}}, Field: []*descriptorpb.FieldDescriptorProto{field("f", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}, Extension: []*descriptorpb.FieldDescriptorProto{field("ext", 100, descriptorpb.FieldDescriptorProto_TYPE_STRING)}, NestedType: []*descriptorpb.DescriptorProto{{Name: proto.String("Entry"), Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)}, Field: []*descriptorpb.FieldDescriptorProto{field("key", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING), field("value", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING)}}}, EnumType: []*descriptorpb.EnumDescriptorProto{{Name: proto.String("E"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("V"), Number: proto.Int32(0)}}}}}
	f := messageFile("proto2", root)
	f.Service = []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("S"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("M")}}}}
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{f}}
	count := 0
	if err := descriptorTree(context.Background(), set.ProtoReflect(), 0, &count); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("declarations %d, want 12", count)
	}
	// Source locations/options are messages but not declarations.
	f.SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{LeadingComments: proto.String(strings.Repeat("x", 10))}}}
	count = 0
	if err := descriptorTree(context.Background(), set.ProtoReflect(), 98, &count); err == nil {
		t.Fatal("options/source tree depth unbounded")
	}
}
