package protobufview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/sdkks/nesdit/internal/omap"
)

func TestIndependentBudgetBoundaries(t *testing.T) {
	for _, stage := range []string{"decoded", "view"} {
		t.Run(stage, func(t *testing.T) {
			b := budget{stage: stage, nodes: nodeLimit - 1, text: textLimit - 1}
			if err := b.node(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			if err := b.addText(1); err != nil {
				t.Fatal(err)
			}
			if err := b.node(context.Background(), 0); err == nil {
				t.Fatal("one-over nodes accepted")
			}
			if err := b.addText(1); err == nil {
				t.Fatal("one-over text accepted")
			}
			b = budget{stage: stage}
			if err := b.node(context.Background(), 101); err == nil {
				t.Fatal("one-over depth accepted")
			}
		})
	}
	d := prepared(t, "google.protobuf.StringValue", wkts()...)
	m := d.message.New()
	m.Set(m.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfString("hello"))
	p := newPreflight(context.Background(), d)
	if err := p.decodedMessage(m, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.viewMessage(m, 0, ""); err != nil {
		t.Fatal(err)
	}
	if p.decoded.nodes != 2 || p.decoded.text != 10 || p.view.nodes != 1 || p.view.text != 5 {
		t.Fatalf("independent shapes: decoded %+v view %+v", p.decoded, p.view)
	}
	// Checking one shape must not consume the other shape's remaining allowance.
	p = newPreflight(context.Background(), d)
	p.decoded.nodes = nodeLimit - 2
	p.view.nodes = nodeLimit - 1
	if err := p.decodedMessage(m, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.viewMessage(m, 0, ""); err != nil {
		t.Fatal(err)
	}
	if p.decoded.nodes != nodeLimit || p.view.nodes != nodeLimit {
		t.Fatal("budget not independently charged")
	}
}

func TestDecodedAndViewDepth(t *testing.T) {
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Node")})
	child := field("child", 1, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	child.TypeName = proto.String(".example.Node")
	f.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{child}
	d := prepared(t, "example.Node", f)
	for _, depth := range []int{100, 101} {
		root := d.message.New()
		current := root
		for i := 0; i < depth; i++ {
			current = current.Mutable(current.Descriptor().Fields().ByNumber(1)).Message()
		}
		p := newPreflight(context.Background(), d)
		e1 := p.decodedMessage(root, 0)
		e2 := p.viewMessage(root, 0, "")
		if (e1 == nil) != (depth == 100) || (e2 == nil) != (depth == 100) {
			t.Fatalf("depth %d decoded=%v view=%v", depth, e1, e2)
		}
		_, err := d.decode(context.Background(), wire(t, root.Interface()))
		if (err == nil) != (depth == 100) {
			t.Fatalf("decode depth %d: %v", depth, err)
		}
	}
	// Scalar roots are depth zero, and scalar children at depth 101 fail.
	b := budget{stage: "actual"}
	if _, err := sortView(context.Background(), omap.StrValue("x"), 100, &b); err != nil {
		t.Fatal(err)
	}
	if _, err := sortView(context.Background(), omap.StrValue("x"), 101, &b); err == nil {
		t.Fatal("scalar depth not checked")
	}
}

func TestWKTActualShapeNotDescriptorShape(t *testing.T) {
	d := prepared(t, "google.protobuf.Value", wkts()...)
	value := structpb.NewStringValue("leaf")
	for i := 0; i < 20; i++ {
		value = structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{value}})
	}
	root := d.message.New()
	if err := (proto.UnmarshalOptions{Resolver: d.types}).Unmarshal(wire(t, value), root.Interface()); err != nil {
		t.Fatal(err)
	}
	p := newPreflight(context.Background(), d)
	if err := p.decodedMessage(root, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.viewMessage(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	if p.view.nodes != 21 || p.decoded.nodes != 62 {
		t.Fatalf("shapes decoded=%d view=%d", p.decoded.nodes, p.view.nodes)
	}
	if p.view.text != 4 {
		t.Fatalf("view charged hidden WKT fields: %d", p.view.text)
	}
	// Descriptor-shaped depth is 61 here, whereas actual view depth is only 20.
	p = newPreflight(context.Background(), d)
	if err := p.viewMessage(root, 80, ""); err != nil {
		t.Fatal(err)
	}
	p = newPreflight(context.Background(), d)
	if err := p.viewMessage(root, 81, ""); err == nil {
		t.Fatal("WKT view depth not bounded")
	}
	p = newPreflight(context.Background(), d)
	if err := p.decodedMessage(root, 40); err == nil {
		t.Fatal("decoded WKT shape depth not bounded")
	}
}

func TestNodeAndTextAccounting(t *testing.T) {
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("List")})
	values := field("values", 1, descriptorpb.FieldDescriptorProto_TYPE_BOOL)
	values.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	f.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{values}
	d := prepared(t, "example.List", f)
	m := d.message.New()
	list := m.Mutable(m.Descriptor().Fields().ByNumber(1)).List()
	for i := 0; i < nodeLimit-2; i++ {
		list.Append(protoreflect.ValueOfBool(false))
	}
	p := newPreflight(context.Background(), d)
	if err := p.decodedMessage(m, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.viewMessage(m, 0, ""); err != nil {
		t.Fatal(err)
	}
	if p.decoded.nodes != nodeLimit || p.view.nodes != nodeLimit {
		t.Fatal("node equality missed")
	}
	list.Append(protoreflect.ValueOfBool(false))
	p = newPreflight(context.Background(), d)
	if err := p.decodedMessage(m, 0); err == nil {
		t.Fatal("decoded one-over accepted")
	}
	if err := p.viewMessage(m, 0, ""); err == nil {
		t.Fatal("view one-over accepted")
	}
	// Large shared strings exercise accounting without depending on wire size.
	sd := prepared(t, "google.protobuf.StringValue", wkts()...)
	sm := sd.message.New()
	sf := sm.Descriptor().Fields().ByNumber(1)
	sm.Set(sf, protoreflect.ValueOfString(strings.Repeat("a", textLimit-len("value"))))
	p = newPreflight(context.Background(), sd)
	if err := p.decodedMessage(sm, 0); err != nil {
		t.Fatal(err)
	}
	if p.decoded.text != textLimit {
		t.Fatal(p.decoded.text)
	}
	sm.Set(sf, protoreflect.ValueOfString(strings.Repeat("a", textLimit-len("value")+1)))
	if err := newPreflight(context.Background(), sd).decodedMessage(sm, 0); err == nil {
		t.Fatal("decoded text one-over accepted")
	}
	// View text limit is independent of the certificate (quotes cost two bytes).
	p = newPreflight(context.Background(), sd)
	p.view.text = textLimit - 3
	if err := p.string("abc", 0); err != nil {
		t.Fatal(err)
	}
	if err := p.string("d", 0); err == nil {
		t.Fatal("view text one-over accepted")
	}
}

func TestCertificateEqualityConservatismAndPostChecks(t *testing.T) {
	p := newPreflight(context.Background(), nil)
	if err := p.size(serializedLimit); err != nil {
		t.Fatal(err)
	}
	if err := checkSerialized(serializedLimit, p.certificate); err != nil {
		t.Fatal(err)
	}
	if err := p.size(1); err == nil {
		t.Fatal("certificate one-over accepted")
	}
	if err := checkSerialized(3, 2); err == nil {
		t.Fatal("certificate postcheck missing")
	}
	if err := checkSerialized(serializedLimit+1, serializedLimit+1); err == nil {
		t.Fatal("absolute postcheck missing")
	}
	// Fixed numeric width is deliberately conservative: a fitting actual JSON
	// value can fail certificate admission before the complete Marshal call.
	f := messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Numbers")})
	values := field("n", 1, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE)
	values.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	f.MessageType[0].Field = []*descriptorpb.FieldDescriptorProto{values}
	d := prepared(t, "example.Numbers", f)
	m := d.message.New()
	list := m.Mutable(m.Descriptor().Fields().ByNumber(1)).List()
	for i := 0; i < 550000; i++ {
		list.Append(protoreflect.ValueOfFloat64(0))
	}
	p = newPreflight(context.Background(), d)
	if err := p.decodedMessage(m, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.viewMessage(m, 0, ""); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want certificate failure, got %v", err)
	}
	actual, err := (protojson.MarshalOptions{Resolver: d.types}).Marshal(m.Interface())
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) >= serializedLimit {
		t.Fatalf("fixture does not demonstrate conservative rejection: %d", len(actual))
	}
	if _, err := d.decode(context.Background(), wire(t, m.Interface())); err == nil {
		t.Fatal("decoder bypassed certificate")
	}
	// Accepted serialization still gets the independent actual-view checks.
	b := budget{stage: "view", nodes: nodeLimit}
	if _, err := sortView(context.Background(), omap.NullValue(), 0, &b); err == nil {
		t.Fatal("actual view node check missing")
	}
	b = budget{stage: "view", text: textLimit}
	if _, err := sortView(context.Background(), omap.StrValue("x"), 0, &b); err == nil {
		t.Fatal("actual view text check missing")
	}
}

func TestAnyAggregateAndDepthDoNotReset(t *testing.T) {
	d := prepared(t, "google.protobuf.Any", wkts()...)
	empty := protowire.AppendBytes(protowire.AppendTag(nil, 100, protowire.BytesType), make([]byte, 7<<19))
	a := &anypb.Any{TypeUrl: "local/google.protobuf.Empty", Value: empty}
	a = &anypb.Any{TypeUrl: "local/google.protobuf.Any", Value: wire(t, a)}
	a = &anypb.Any{TypeUrl: "local/google.protobuf.Any", Value: wire(t, a)}
	_, err := d.decode(context.Background(), wire(t, a))
	if err == nil || !strings.Contains(err.Error(), "aggregate Any wire") {
		t.Fatalf("aggregate expansion not checked: %v", err)
	}
	a = &anypb.Any{}
	for i := 0; i < 100; i++ {
		a = &anypb.Any{TypeUrl: "local/google.protobuf.Any", Value: wire(t, a)}
	}
	if _, err := d.decode(context.Background(), wire(t, a)); err != nil {
		t.Fatalf("inclusive Any depth: %v", err)
	}
	a = &anypb.Any{TypeUrl: "local/google.protobuf.Any", Value: wire(t, a)}
	if _, err := d.decode(context.Background(), wire(t, a)); err == nil {
		t.Fatal("Any depth reset")
	}
	// Exact aggregate wire allowance is inclusive; charge before decoding.
	root := d.message.New()
	fd := root.Descriptor().Fields()
	target := wire(t, wrapperspb.Bool(true))
	root.Set(fd.ByNumber(1), protoreflect.ValueOfString("local/google.protobuf.BoolValue"))
	root.Set(fd.ByNumber(2), protoreflect.ValueOfBytes(target))
	p := newPreflightForTest(d, RawLimit-len(target))
	if err := p.decodedMessage(root, 0); err != nil {
		t.Fatal(err)
	}
	p = newPreflightForTest(d, RawLimit-len(target)+1)
	if err := p.decodedMessage(root, 0); err == nil {
		t.Fatal("Any wire one-over accepted")
	}
}
func newPreflightForTest(d *decoder, wire int) *preflight {
	p := newPreflight(context.Background(), d)
	p.anyWire = wire
	return p
}

func TestCancellationCheckpoints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepare(ctx, nil, "example.Event"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	d := prepared(t, "google.protobuf.StringValue", wkts()...)
	if _, err := d.decode(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p := newPreflight(ctx, d)
	if err := p.decodedMessage(d.message.New(), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := p.viewMessage(d.message.New(), 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
