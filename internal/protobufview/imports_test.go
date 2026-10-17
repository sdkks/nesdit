package protobufview

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func publicImportFixture() []*descriptorpb.FileDescriptorProto {
	leaf := &descriptorpb.DescriptorProto{Name: proto.String("Leaf"), ExtensionRange: []*descriptorpb.DescriptorProto_ExtensionRange{{Start: proto.Int32(100), End: proto.Int32(200)}}}
	leaf.Field = []*descriptorpb.FieldDescriptorProto{field("text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}
	a := messageFile("proto2", leaf, &descriptorpb.DescriptorProto{Name: proto.String("Extra"), Field: []*descriptorpb.FieldDescriptorProto{field("text", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING)}})
	a.Name = proto.String("a.proto")
	a.EnumType = []*descriptorpb.EnumDescriptorProto{{Name: proto.String("Mode"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String("ZERO"), Number: proto.Int32(0)}, {Name: proto.String("ONE"), Number: proto.Int32(1)}}}}
	b := &descriptorpb.FileDescriptorProto{Name: proto.String("b.proto"), Dependency: []string{"a.proto"}, PublicDependency: []int32{0}}
	c := &descriptorpb.FileDescriptorProto{Name: proto.String("c.proto"), Dependency: []string{"b.proto"}, PublicDependency: []int32{0}}
	msg := field("leaf", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	msg.TypeName = proto.String(".example.Leaf")
	enum := field("mode", 2, descriptorpb.FieldDescriptorProto_TYPE_ENUM)
	enum.TypeName = proto.String(".example.Mode")
	enum.DefaultValue = proto.String("ONE")
	root := messageFile("proto2", &descriptorpb.DescriptorProto{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{msg, enum}})
	root.Name = proto.String("root.proto")
	root.Dependency = []string{"c.proto"}
	ext := field("leaf_extra", 100, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	ext.Extendee = proto.String(".example.Leaf")
	ext.TypeName = proto.String(".example.Extra")
	root.Extension = []*descriptorpb.FieldDescriptorProto{ext}
	root.Service = []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("S"), Method: []*descriptorpb.MethodDescriptorProto{{Name: proto.String("Call"), InputType: proto.String(".example.Leaf"), OutputType: proto.String(".example.Event")}}}}
	return []*descriptorpb.FileDescriptorProto{root, c, b, a} // reverse dependency order
}

func TestPublicImportVisibilityAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name string
		ok   bool
		edit func([]*descriptorpb.FileDescriptorProto)
	}{
		{"transitive-public", true, func(_ []*descriptorpb.FileDescriptorProto) {}},
		{"relative-name", true, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].MessageType[0].Field[0].TypeName = proto.String("Leaf")
		}},
		{"direct-and-transitive-overlap", true, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].Dependency = append(fs[0].Dependency, "a.proto") }},
		{"public-diamond", true, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[1].Dependency = []string{"a.proto"}
			fs[0].Dependency = []string{"b.proto", "c.proto"}
		}},
		{"private-leaf", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[2].PublicDependency = nil }},
		{"private-middle", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[1].PublicDependency = nil }},
		{"option-only", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].OptionDependency, fs[0].Dependency = fs[0].Dependency, nil
		}},
		{"option-target-not-reexported", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[2].OptionDependency, fs[2].Dependency = fs[2].Dependency, nil
			fs[2].PublicDependency = nil
		}},
		{"independent-option", true, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[1].OptionDependency, fs[1].Dependency = []string{"a.proto"}, nil
			fs[1].PublicDependency = nil
			fs[0].Dependency = []string{"b.proto", "c.proto"}
		}},
		{"option-public-overlap", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].OptionDependency = []string{"a.proto"} }},
		{"option-direct-overlap", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].OptionDependency = []string{"c.proto"} }},
		{"duplicate-option", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].Dependency = []string{"a.proto"}
			fs[0].OptionDependency = []string{"c.proto", "c.proto"}
		}},
		{"missing-option", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].OptionDependency = []string{"absent.proto"} }},
		{"missing-ordinary", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].Dependency = append(fs[0].Dependency, "absent.proto")
		}},
		{"negative-public-index", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[2].PublicDependency = []int32{-1} }},
		{"out-of-range-public-index", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[2].PublicDependency = []int32{1} }},
		{"duplicate-public-index", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[2].PublicDependency = []int32{0, 0} }},
		{"duplicate-direct", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[2].Dependency = []string{"a.proto", "a.proto"} }},
		{"duplicate-public-path", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[2].Dependency = []string{"a.proto", "a.proto"}
			fs[2].PublicDependency = []int32{0, 1}
		}},
		{"ordinary-cycle", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[3].Dependency = []string{"root.proto"} }},
		{"option-cycle", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[3].OptionDependency = []string{"root.proto"} }},
		{"self-import", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[3].Dependency = []string{"a.proto"} }},
		{"self-option", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[3].OptionDependency = []string{"a.proto"} }},
		{"unresolved-type", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].MessageType[0].Field[0].TypeName = proto.String(".example.Missing")
		}},
		{"unimported-type", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].Dependency = nil }},
		{"wrong-kind", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].MessageType[0].Field[0].TypeName = proto.String(".example.Mode")
		}},
		{"invalid-field-number", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].MessageType[0].Field[0].Number = proto.Int32(0) }},
		{"invalid-default", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].MessageType[0].Field[1].DefaultValue = proto.String("MISSING")
		}},
		{"invalid-service", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].Service[0].Method[0].InputType = proto.String(".example.Missing")
		}},
		{"invalid-extension", false, func(fs []*descriptorpb.FileDescriptorProto) { fs[0].Extension[0].Number = proto.Int32(99) }},
		{"invalid-source-span", false, func(fs []*descriptorpb.FileDescriptorProto) {
			fs[0].SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{Span: []int32{1, 2}}}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fs := publicImportFixture()
			tt.edit(fs)
			data := descriptorBytes(t, fs...)
			before := bytes.Clone(data)
			d, err := prepare(context.Background(), data, "example.Event")
			if (err == nil) != tt.ok {
				t.Fatalf("success=%v want=%v: %v", err == nil, tt.ok, err)
			}
			if !bytes.Equal(data, before) || !bytes.Equal(data, descriptorBytes(t, fs...)) {
				t.Fatal("input mutated")
			}
			if tt.ok {
				checkPublicImportIdentity(t, d)
			}
		})
	}
}

func checkPublicImportIdentity(t *testing.T, d *decoder) {
	t.Helper()
	msg := d.message.New()
	fd := msg.Descriptor().Fields().ByNumber(1)
	leaf := msg.Mutable(fd).Message()
	mt, err := d.types.FindMessageByName("example.Leaf")
	if err != nil || fd.Message() != mt.Descriptor() || leaf.Descriptor() != mt.Descriptor() {
		t.Fatal("message identity changed", err)
	}
	leaf.Set(leaf.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("payload"))
	xt, err := d.types.FindExtensionByNumber("example.Leaf", 100)
	if err != nil {
		t.Fatal(err)
	}
	if xt.TypeDescriptor().ContainingMessage() != mt.Descriptor() {
		t.Fatal("extendee identity changed")
	}
	em, err := d.types.FindEnumByName("example.Mode")
	if err != nil || msg.Descriptor().Fields().ByNumber(2).Enum() != em.Descriptor() || msg.Descriptor().Fields().ByNumber(2).DefaultEnumValue() != em.Descriptor().Values().ByName("ONE") {
		t.Fatal("enum/default identity changed", err)
	}
	method := msg.Descriptor().ParentFile().Services().Get(0).Methods().Get(0)
	if method.Input() != mt.Descriptor() || method.Output() != msg.Descriptor() {
		t.Fatal("service identity changed")
	}
	extra := dynamicpb.NewMessage(xt.TypeDescriptor().Message())
	extra.Set(extra.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("extension"))
	leaf.Set(xt.TypeDescriptor(), protoreflect.ValueOfMessage(extra))
	checkJSON(t, d, wire(t, msg.Interface()), `{"leaf":{"[example.leaf_extra]":{"text":"extension"},"text":"payload"}}`)
}

func TestPublicImportSupportedSyntax(t *testing.T) {
	for _, syntax := range []string{"proto2", "proto3", "2023", "2024"} {
		t.Run(syntax, func(t *testing.T) {
			fs := publicImportFixture()
			fs[0].Extension = nil
			fs[0].MessageType[0].Field = fs[0].MessageType[0].Field[:1]
			fs[3].EnumType = nil
			fs[3].MessageType = fs[3].MessageType[:1]
			fs[3].MessageType[0].ExtensionRange = nil
			for _, f := range fs {
				f.Syntax = proto.String(syntax)
				if syntax == "2023" || syntax == "2024" {
					f.Syntax = proto.String("editions")
					f.Edition = descriptorpb.Edition_EDITION_2023.Enum()
					if syntax == "2024" {
						f.Edition = descriptorpb.Edition_EDITION_2024.Enum()
					}
				}
			}
			data := descriptorBytes(t, fs...)
			path := filepath.Join(t.TempDir(), "set.pb")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			decode, err := Prepare(context.Background(), path, "example.Event")
			if err != nil {
				t.Fatal(err)
			}
			v, err := decode(context.Background(), []byte{10, 3, 10, 1, 'x'})
			if err != nil || valueJSON(t, v) != `{"leaf":{"text":"x"}}` {
				t.Fatalf("payload=%v err=%v", v, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("descriptor file mutated", err)
			}
		})
	}
}

func TestConstructionFileCloneAndClosure(t *testing.T) {
	files := publicImportFixture()
	f := files[0]
	// Deliberately non-ordinal direct order must remain an unchanged prefix.
	f.Dependency = []string{"c.proto", "a.proto"}
	f.PublicDependency = []int32{0}
	f.OptionDependency = []string{"options.proto"}
	f.WeakDependency = []int32{1}
	f.Options = &descriptorpb.FileOptions{JavaPackage: proto.String("sample.namespace")}
	f.SourceCodeInfo = &descriptorpb.SourceCodeInfo{Location: []*descriptorpb.SourceCodeInfo_Location{{Path: []int32{3, 0}, Span: []int32{1, 2, 3}, LeadingComments: proto.String("public import")}}}
	f.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	before := proto.Clone(f)
	index := map[string]int{"root.proto": 0, "c.proto": 1, "b.proto": 2, "a.proto": 3}
	public := [][]int{nil, {2, 3}, {3}, nil}
	n, closure, err := constructionFile(context.Background(), f, files, index, public)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.PublicDependency) != 0 || !slices.Equal(n.Dependency, []string{"c.proto", "a.proto", "b.proto"}) || !slices.Equal(closure, []int{1, 2, 3}) {
		t.Fatal(n.Dependency, n.PublicDependency, closure)
	}
	n.Dependency = slices.Clone(f.Dependency)
	n.PublicDependency = slices.Clone(f.PublicDependency)
	if !proto.Equal(n, before) || !proto.Equal(f, before) {
		t.Fatal("original or non-import clone content changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := constructionFile(ctx, f, files, index, public); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func publicImportGraph(n, levels int) []*descriptorpb.FileDescriptorProto {
	fs := make([]*descriptorpb.FileDescriptorProto, n)
	for i := range fs {
		f := &descriptorpb.FileDescriptorProto{Name: proto.String(fmt.Sprintf("file%d.proto", i)), Syntax: proto.String("proto3"), Package: proto.String("graph"), MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String(fmt.Sprintf("Node%d", i))}}}
		for j := 0; j < i; j++ {
			if levels > 0 && j%levels >= i%levels {
				continue
			}
			f.Dependency = append(f.Dependency, fs[j].GetName())
			f.PublicDependency = append(f.PublicDependency, int32(len(f.Dependency)-1))
		}
		fs[i] = f
	}
	return fs
}

func checkNormalizedGraph(t *testing.T, fs []*descriptorpb.FileDescriptorProto) {
	t.Helper()
	data := descriptorBytes(t, fs...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	d, err := prepare(ctx, data, "graph.Node0")
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	checkJSON(t, d, nil, `{}`)
	edges := 0
	for i, f := range fs {
		mt, err := d.types.FindMessageByName(protoreflect.FullName(fmt.Sprintf("graph.Node%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		fd := mt.Descriptor().ParentFile()
		if fd.Path() != f.GetName() {
			t.Fatal("file identity changed")
		}
		imps := fd.Imports()
		if imps.Len() > len(fs)-1 || imps.Len() < len(f.Dependency) {
			t.Fatal("normalized import count out of bounds", imps.Len())
		}
		seen := make(map[string]bool)
		for j := 0; j < imps.Len(); j++ {
			imp := imps.Get(j)
			if imp.IsPublic || seen[imp.Path()] || imp.Path() == fd.Path() {
				t.Fatal("public, duplicate or self edge remains")
			}
			seen[imp.Path()] = true
			if j < len(f.Dependency) && imp.Path() != f.Dependency[j] {
				t.Fatal("original import prefix changed")
			}
		}
		edges += imps.Len()
	}
	t.Logf("files=%d bytes=%d normalized-edges=%d public-edges=0 preparation=%s", len(fs), len(data), edges, elapsed)
}

func TestPublicImportBoundedGrowth(t *testing.T) {
	// Only normalized construction is exercised here; test execution is also
	// bounded by go test's timeout, since library calls are not cancellable.
	for _, n := range []int{16, 20, 24, 28, 48, 80, 101, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			levels := 0
			if n == 256 {
				levels = 32 // stays within the original import-height limit
			}
			checkNormalizedGraph(t, publicImportGraph(n, levels))
		})
	}
	if _, err := prepare(context.Background(), descriptorBytes(t, publicImportGraph(102, 0)...), "graph.Node0"); err == nil || !strings.Contains(err.Error(), "import depth") {
		t.Fatalf("original depth 101 must fail before normalization: %v", err)
	}
}

func TestPublicImportLongPath(t *testing.T) {
	fs := publicImportGraph(101, 0)
	long := strings.Repeat("a", 512<<10) + ".proto"
	fs[0].Name = proto.String(long)
	fs[1].Dependency[0] = long
	// Only the first importer names the long path; later files see it publicly.
	for i := 2; i < len(fs); i++ {
		fs[i].Dependency = fs[i].Dependency[1:]
		fs[i].PublicDependency = fs[i].PublicDependency[:len(fs[i].Dependency)]
	}
	checkNormalizedGraph(t, fs)
}
