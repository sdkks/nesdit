package protobufview

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/sdkks/nesdit/internal/omap"
)

// Prepare freezes a self-contained descriptor set and exact message selection.
// The returned decoder owns fresh messages and budgets for every invocation.
func Prepare(ctx context.Context, path, name string) (func(context.Context, []byte) (omap.Value, error), error) {
	data, err := ReadFile(ctx, path, descriptorLimit)
	if err != nil {
		return nil, err
	}
	d, err := prepare(ctx, data, name)
	if err != nil {
		return nil, err
	}
	return d.decode, nil
}

type decoder struct {
	types   *dynamicpb.Types
	message protoreflect.MessageType
}

func prepare(ctx context.Context, data []byte, name string) (*decoder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > descriptorLimit {
		return nil, errors.New("descriptor: exceeds 4 MiB")
	}
	if !protoreflect.FullName(name).IsValid() {
		return nil, errors.New("descriptor: message type must be an exact full name")
	}
	set := new(descriptorpb.FileDescriptorSet)
	// Never let descriptor options consult generated or application registries.
	err := (proto.UnmarshalOptions{Resolver: new(protoregistry.Types), RecursionLimit: depthLimit + 1}).Unmarshal(data, set)
	if err != nil {
		return nil, errors.New("descriptor: invalid binary descriptor set or depth exceeds 100")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(set.File) > 256 {
		return nil, errors.New("descriptor: exceeds 256 files")
	}
	declarations := 0
	if err := descriptorTree(ctx, set.ProtoReflect(), 0, &declarations); err != nil {
		return nil, err
	}
	files := make(map[string]*descriptorpb.FileDescriptorProto, len(set.File))
	index := make(map[string]int, len(set.File))
	for i, f := range set.File {
		if f.GetName() == "" || files[f.GetName()] != nil {
			return nil, errors.New("descriptor: missing or duplicate file name")
		}
		switch f.GetSyntax() {
		case "", "proto2", "proto3":
			if f.Edition != nil {
				return nil, errors.New("descriptor: edition requires editions syntax")
			}
		case "editions":
			if f.GetEdition() != descriptorpb.Edition_EDITION_2023 && f.GetEdition() != descriptorpb.Edition_EDITION_2024 {
				return nil, errors.New("descriptor: unsupported edition")
			}
		default:
			return nil, errors.New("descriptor: unsupported syntax")
		}
		files[f.GetName()] = f
		index[f.GetName()] = i
	}
	registry := new(protoregistry.Files)
	visiting := make(map[string]bool)
	heights := make(map[string]int)
	public := make([][]int, len(set.File))
	var add func(string) (int, error)
	add = func(path string) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if h := heights[path]; h != 0 {
			return h, nil
		}
		f := files[path]
		if f == nil || visiting[path] {
			return 0, errors.New("descriptor: missing import or import cycle")
		}
		if len(visiting) > depthLimit {
			return 0, errors.New("descriptor: import depth exceeds 100")
		}
		visiting[path] = true
		height := 1
		deps := append(append([]string(nil), f.Dependency...), f.OptionDependency...)
		for _, dep := range deps {
			h, err := add(dep)
			if err != nil {
				return 0, err
			}
			height = max(height, h+1)
		}
		delete(visiting, path)
		if height > depthLimit+1 {
			return 0, errors.New("descriptor: import depth exceeds 100")
		}
		normalized, closure, err := constructionFile(ctx, f, set.File, index, public)
		if err != nil {
			return 0, err
		}
		fd, err := protodesc.NewFile(normalized, registry)
		if err != nil {
			return 0, errors.New("descriptor: invalid definitions or unresolved references")
		}
		if err := registry.RegisterFile(fd); err != nil {
			return 0, errors.New("descriptor: conflicting definitions")
		}
		public[index[path]] = closure
		heights[path] = height
		return height, nil
	}
	for _, f := range set.File {
		if _, err := add(f.GetName()); err != nil {
			return nil, err
		}
	}
	if err := validateDefinitions(ctx, registry); err != nil {
		return nil, err
	}
	types := dynamicpb.NewTypes(registry)
	mt, err := types.FindMessageByName(protoreflect.FullName(name))
	if err != nil {
		return nil, errors.New("descriptor: selected message type is not defined locally")
	}
	return &decoder{types: types, message: mt}, nil
}

// Every descriptor message descends, including options and source locations.
// Only the listed declaration messages charge the declaration counter.
func descriptorTree(ctx context.Context, m protoreflect.Message, depth int, count *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > depthLimit {
		return errors.New("descriptor: message tree depth exceeds 100")
	}
	switch m.Descriptor().FullName() {
	case "google.protobuf.FileDescriptorProto", "google.protobuf.DescriptorProto", "google.protobuf.FieldDescriptorProto", "google.protobuf.OneofDescriptorProto", "google.protobuf.EnumDescriptorProto", "google.protobuf.EnumValueDescriptorProto", "google.protobuf.ServiceDescriptorProto", "google.protobuf.MethodDescriptorProto":
		*count++
		if *count > 10_000 {
			return errors.New("descriptor: exceeds 10000 declarations")
		}
	}
	var result error
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.Kind() != protoreflect.MessageKind {
			return true
		}
		if f.IsList() {
			for i := 0; i < v.List().Len(); i++ {
				if result = descriptorTree(ctx, v.List().Get(i).Message(), depth+1, count); result != nil {
					return false
				}
			}
		} else {
			result = descriptorTree(ctx, v.Message(), depth+1, count)
		}
		return result == nil
	})
	return result
}

func validateDefinitions(ctx context.Context, files *protoregistry.Files) error {
	names := make(map[protoreflect.FullName]map[string]bool)
	numbers := make(map[protoreflect.FullName]map[protoreflect.FieldNumber]bool)
	var result error
	addName := func(m protoreflect.FullName, name string) error {
		if name == "@type" || strings.ContainsRune(name, 0) || !utf8.ValidString(name) {
			return errors.New("descriptor: invalid or reserved JSON field name")
		}
		if names[m] == nil {
			names[m] = make(map[string]bool)
		}
		if names[m][name] {
			return errors.New("descriptor: ambiguous JSON field names")
		}
		names[m][name] = true
		return nil
	}
	extensions := func(xs protoreflect.ExtensionDescriptors) error {
		for i := 0; i < xs.Len(); i++ {
			x := xs.Get(i)
			m := x.ContainingMessage().FullName()
			if numbers[m] == nil {
				numbers[m] = make(map[protoreflect.FieldNumber]bool)
			}
			if numbers[m][x.Number()] {
				return errors.New("descriptor: duplicate extension number")
			}
			numbers[m][x.Number()] = true
			if err := addName(m, x.JSONName()); err != nil {
				return err
			}
		}
		return nil
	}
	enums := func(es protoreflect.EnumDescriptors) error {
		for i := 0; i < es.Len(); i++ {
			e := es.Get(i)
			if e.FullName() == "google.protobuf.NullValue" && (e.Syntax() != protoreflect.Proto3 || e.Values().Len() != 1 || e.Values().Get(0).Name() != "NULL_VALUE" || e.Values().Get(0).Number() != 0) {
				return errors.New("descriptor: invalid well-known enum shape")
			}
		}
		return nil
	}
	var messages func(protoreflect.MessageDescriptors) error
	messages = func(ms protoreflect.MessageDescriptors) error {
		for i := 0; i < ms.Len(); i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			m := ms.Get(i)
			if o, ok := m.Options().(*descriptorpb.MessageOptions); ok && o.GetMessageSetWireFormat() {
				return errors.New("descriptor: MessageSet is unsupported")
			}
			if err := validateWKT(m); err != nil {
				return err
			}
			for j := 0; j < m.Fields().Len(); j++ {
				if err := addName(m.FullName(), m.Fields().Get(j).JSONName()); err != nil {
					return err
				}
			}
			if err := extensions(m.Extensions()); err != nil {
				return err
			}
			if err := enums(m.Enums()); err != nil {
				return err
			}
			if err := messages(m.Messages()); err != nil {
				return err
			}
		}
		return nil
	}
	files.RangeFiles(func(f protoreflect.FileDescriptor) bool {
		if result = ctx.Err(); result != nil {
			return false
		}
		if result = extensions(f.Extensions()); result != nil {
			return false
		}
		if result = enums(f.Enums()); result != nil {
			return false
		}
		result = messages(f.Messages())
		return result == nil
	})
	return result
}
