package yaml

// Test_YAMLWalker_CircularAlias_Detected exercises the TASK-0018 S-3
// defense-in-depth cycle guard in yamlWalker. yaml.v3 currently refuses to
// produce circular nodes from YAML text input, so this test constructs a
// circular *yaml.Node graph programmatically and drives the walker directly.
//
// This test lives in package yaml (not yaml_test) so it can access the
// unexported yamlWalker type and construct the cycle. If yaml.v3 is ever
// replaced by a library that does permit circular aliases, this test would
// catch an infinite recursion before it becomes a crash or OOM.

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func Test_YAMLWalker_CircularAlias_Detected(t *testing.T) {
	t.Parallel()

	// Construct a circular alias: nodeA.Alias → nodeB, nodeB.Alias → nodeA.
	// Both are AliasNodes pointing at each other, forming a 2-cycle.
	nodeA := &yaml.Node{Kind: yaml.AliasNode}
	nodeB := &yaml.Node{Kind: yaml.AliasNode}
	nodeA.Alias = nodeB
	nodeB.Alias = nodeA

	w := &yamlWalker{maxDepth: 100, maxNodes: 100_000}
	_, err := w.node(nodeA, 0)
	if err == nil {
		t.Fatal("expected circular alias error; got nil — cycle detection is broken")
	}
	if !strings.Contains(err.Error(), "circular alias") {
		t.Errorf("error message %q does not contain 'circular alias'", err.Error())
	}
}
