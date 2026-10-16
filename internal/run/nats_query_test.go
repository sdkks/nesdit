package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/natstest"
	"github.com/sdkks/nesdit/internal/query"
)

func TestNATSQueryEvents(t *testing.T) {
	for _, tc := range []struct{ body, filter, project, payload, failure string }{
		{`{"id":2,"subject":"body","headers":null}`, `true`, `{id: .id}`, `{"id":2}`, ""},
		{`{"id":2}`, `.id = 99 | true`, `.id`, `2`, ""},
		{`12.50`, `true`, "", `12.50`, ""},
		{`null`, "", `null`, `null`, ""}, {`true`, "", `[.,false]`, `[true,false]`, ""},
		{`"text"`, "", `.`, `"text"`, ""}, {`false`, "", `.`, `false`, ""},
		{`null`, `false`, `error("PRIVATE")`, "", ""},
		{`{`, `false`, `.`, "", "decode:"},
		{`{}`, `.missing`, `.`, "", "filter: result:"},
		{`{}`, `true`, `empty`, "", "project: result:"},
		{`{}`, `true,error("PRIVATE")`, `.`, "", "filter: runtime:"},
		{`{}`, `true`, `.,error("PRIVATE")`, "", "project: runtime:"},
		{`{}`, `true`, `"<" * 3000000`, "", "encode:"},
	} {
		t.Run(tc.body+"/"+tc.filter+"/"+tc.project, func(t *testing.T) {
			var filter, project *query.Inspection
			var err error
			if tc.filter != "" {
				filter, err = query.CompileInspection(context.Background(), tc.filter)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.project != "" {
				project, err = query.CompileInspection(context.Background(), tc.project)
				if err != nil {
					t.Fatal(err)
				}
			}
			record := natsinspect.Record{Subject: "orders.created", Headers: map[string][]string{"Trace": {"a", "b"}}, Data: []byte(tc.body)}
			data, _, err := natsQueriedEvent(context.Background(), record, false, false, filter, project)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) || strings.Contains(err.Error(), "PRIVATE") || data != nil {
					t.Fatal(string(data), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.payload != "" {
				want = "{\"subject\":\"orders.created\",\"headers\":{\"Trace\":[\"a\",\"b\"]},\"payload\":" + tc.payload + "}\n"
			}
			if string(data) != want {
				t.Fatalf("got %q want %q", data, want)
			}
		})
	}
}

func TestNATSQuerySession(t *testing.T) {
	for _, tc := range []struct {
		name, filter, project, want string
		fail                        bool
		emitted                     int
	}{
		{"false-count", "false", `error("PRIVATE")`, "reason=count; admitted=3 emitted=0 unfinished_event=false", false, 0},
		{"projection-failure", "true", `if . == 2 then error("PRIVATE") else . end`, "project: runtime:", true, 1},
		{"filter-deadline", `if . == 2 then (def forever: forever; forever) else true end`, ".", "reason=duration; admitted=2 emitted=1 unfinished_event=true", false, 1},
		{"project-deadline", "true", `if . == 2 then (., (def forever: forever; forever)) else . end`, "reason=duration; admitted=2 emitted=1 unfinished_event=true", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &natsFake{records: []natsinspect.Record{{Subject: "x", Data: []byte(`1`)}, {Subject: "x", Data: []byte(`2`)}, {Subject: "x", Data: []byte(`3`)}, {Subject: "x", Data: []byte(`4`)}}}
			var out, logs bytes.Buffer
			err := fakeNATSRun(context.Background(), f, natsOptions{duration: 100 * time.Millisecond, count: 3, filter: tc.filter, project: tc.project}, &out, &logs)
			if (err != nil) != tc.fail || !strings.Contains(logs.String(), tc.want) || strings.Count(out.String(), "\n") != tc.emitted || strings.Contains(logs.String(), "PRIVATE") {
				t.Fatal(err, out.String(), logs.String())
			}
			if tc.name == "false-count" && f.calls != 3 {
				t.Fatal(f.calls)
			}
		})
	}
}

func TestNATSProjectedOutputBoundary(t *testing.T) {
	record := natsinspect.Record{Subject: "x", Data: []byte(`""`)}
	empty, _, err := natsEvent(context.Background(), record, false, false)
	if err != nil {
		t.Fatal(err)
	}
	remaining := natsEventLimit - len(empty)
	for _, extra := range []int{0, 1} {
		expr := fmt.Sprintf(`("<" * %d) + ("x" * %d)`, remaining/6, remaining%6+extra)
		project, err := query.CompileInspection(context.Background(), expr)
		if err != nil {
			t.Fatal(err)
		}
		data, _, err := natsQueriedEvent(context.Background(), record, false, false, nil, project)
		if extra == 0 {
			if err != nil || len(data) != natsEventLimit {
				t.Fatal(len(data), err)
			}
		} else if err == nil || data != nil || !strings.Contains(err.Error(), "encode:") {
			t.Fatal(len(data), err)
		}
	}
}

func TestNATSQueryPreconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runNATS(RunOptions{Ctx: ctx, Logger: logx.New(io.Discard)}, natsOptions{filter: "true"}, nil,
		func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			t.Fatal("connected after cancellation")
			return nil, nil
		})
	if _, ok := err.(*natsCancelled); !ok {
		t.Fatal(err)
	}
	for _, n := range []natsOptions{
		{filterSet: true}, {projectSet: true}, {filter: " \t"}, {project: "["}, {filter: strings.Repeat("x", (1<<20)+1)}, {project: "unknown_function"},
	} {
		opened := false
		err := runNATS(RunOptions{Ctx: context.Background(), Stdout: io.Discard, Logger: logx.New(io.Discard)}, n, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			opened = true
			return &natsFake{}, nil
		})
		if err == nil || opened {
			t.Fatal(err, opened)
		}
	}
	for _, flag := range []string{"--filter", "--project"} {
		for _, args := range [][]string{{flag, "true"}, {"--nats", "x", "--server", "nats://127.0.0.1:1", "--duration", "1s", flag, ""}} {
			var out, logs bytes.Buffer
			if code := Execute(RunOptions{Args: args, Stdout: &out, Stderr: &logs}); code != 1 || out.Len() != 0 || strings.Contains(logs.String(), "connect:") {
				t.Fatal(code, out.String(), logs.String())
			}
		}
	}
}

func TestNATSRealCLIQueries(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "")
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, allFalse := range []bool{false, true} {
		t.Run(fmt.Sprintf("all-false-%t", allFalse), func(t *testing.T) {
			publish := func() {
				for id := 1; id <= 3; id++ {
					body := fmt.Sprintf(`{"id":%d,"show":%t,"subject":"body","headers":"body"}`, id, !allFalse && id > 1)
					if err := pub.PublishMsg(&nats.Msg{Subject: fmt.Sprintf("orders.id%d", id), Header: nats.Header{"Trace": {"a", "b"}}, Data: []byte(body)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
			}
			out, logs, code := observeCLI(t, bin, broker.URL, []string{"--count", "3", "--filter", ".show == true", "--project", "{id: .id}"}, publish)
			want := ""
			emitted := 0
			if !allFalse {
				emitted = 2
				for id := 2; id <= 3; id++ {
					want += fmt.Sprintf("{\"subject\":\"orders.id%d\",\"headers\":{\"Trace\":[\"a\",\"b\"]},\"payload\":{\"id\":%d}}\n", id, id)
				}
			}
			if code != 0 || out != want || !strings.Contains(logs, fmt.Sprintf("reason=count; admitted=3 emitted=%d unfinished_event=false", emitted)) {
				t.Fatal(code, out, logs)
			}
			t.Logf("stdout=%s stderr=%s exit=%d", out, logs, code)
		})
	}
	for _, projected := range []string{`{"new":2}`, `[2,true]`, `"text"`, `12`, `true`, `null`} {
		out, logs, code := observeCLI(t, bin, broker.URL, []string{"--count", "1", "--project", projected}, func() {
			if err := pub.Publish("orders.value", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := pub.FlushTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
		})
		if code != 0 || out != "{\"subject\":\"orders.value\",\"headers\":{},\"payload\":"+projected+"}\n" {
			t.Fatal(code, out, logs)
		}
	}
	t.Run("retained-before-projection-error", func(t *testing.T) {
		out, logs, code := observeCLI(t, bin, broker.URL, []string{"--project", `if . == 2 then error("PRIVATE") else . end`}, func() {
			for _, body := range []string{"1", "2", "3"} {
				if err := pub.Publish("orders.x", []byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := pub.FlushTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
		})
		if code != 1 || out != "{\"subject\":\"orders.x\",\"headers\":{},\"payload\":1}\n" || !strings.Contains(logs, "admitted=2 emitted=1") || strings.Contains(logs, "PRIVATE") {
			t.Fatal(code, out, logs)
		}
	})
	for _, tc := range []struct{ flag, expr, body, stage string }{
		{"--filter", `empty`, `{}`, "filter: result:"},
		{"--filter", `true,false`, `{}`, "filter: result:"},
		{"--filter", `.missing`, `{}`, "filter: result:"},
		{"--project", `empty`, `{}`, "project: result:"},
		{"--project", `1,2`, `{}`, "project: result:"},
		{"--filter", `true,error("PRIVATE")`, `{}`, "filter: runtime:"},
		{"--project", `.,error("PRIVATE")`, `{}`, "project: runtime:"},
		{"--filter", `false`, `{`, "decode:"},
		{"--project", `"<" * 3000000`, `{}`, "encode:"},
	} {
		out, logs, code := observeCLI(t, bin, broker.URL, []string{tc.flag, tc.expr}, func() {
			if err := pub.Publish("orders.x", []byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			if err := pub.FlushTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
		})
		if code != 1 || out != "" || !strings.Contains(logs, tc.stage) || strings.Contains(logs, "PRIVATE") {
			t.Fatal(code, out, logs)
		}
	}
}
