package protobufview

import (
	"context"
	"errors"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// constructionFile normalizes only private constructor input, after original
// byte/tree/declaration/import-height checks. Public closures describe the
// original graph, never the normalized imports: private imports must not leak.
// File ordinals bound unions without repeatedly hashing long supplied paths.
func constructionFile(ctx context.Context, f *descriptorpb.FileDescriptorProto, files []*descriptorpb.FileDescriptorProto, index map[string]int, public [][]int) (*descriptorpb.FileDescriptorProto, []int, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	direct := make([]int, len(f.Dependency))
	visible := make([]bool, len(files))
	for i, path := range f.Dependency {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		id, ok := index[path]
		if !ok || visible[id] {
			return nil, nil, errors.New("descriptor: missing or duplicate ordinary import")
		}
		direct[i] = id
		visible[id] = true
	}
	// Validate original indices and duplicates before removing public flags.
	publicIndex := make([]bool, len(direct))
	exported := make([]bool, len(files))
	for _, i := range f.PublicDependency {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if i < 0 || int(i) >= len(direct) || publicIndex[i] {
			return nil, nil, errors.New("descriptor: invalid or duplicate public import index")
		}
		publicIndex[i] = true
		dep := direct[i]
		exported[dep] = true
		for _, id := range public[dep] {
			exported[id] = true
		}
	}
	var closure []int
	for id, included := range exported {
		if included {
			closure = append(closure, id)
		}
	}

	// Preserve the direct-import prefix/order and every non-import field.
	// Explicit visibility with no public flags avoids the library constructor's
	// recursive public-path traversal; these runtime imports are not exported.
	normalized := proto.Clone(f).(*descriptorpb.FileDescriptorProto)
	normalized.PublicDependency = nil
	extra := make([]bool, len(files))
	for _, dep := range direct {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for _, id := range public[dep] {
			if !visible[id] {
				extra[id] = true
			}
		}
	}
	for id, included := range extra {
		if included {
			normalized.Dependency = append(normalized.Dependency, files[id].GetName())
		}
	}
	return normalized, closure, nil
}
