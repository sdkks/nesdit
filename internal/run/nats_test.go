package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
)

func TestNATSEventValues(t *testing.T) {
	for _, body := range []string{`null`, `true`, `12.50`, `"hello"`, `[1,false]`, "{\n\"subject\":\"body\",\"headers\":null\n}"} {
		t.Run(body, func(t *testing.T) {
			record := natsinspect.Record{Subject: "orders.created", Headers: map[string][]string{"z": {}, "Trace": {"a", "b"}, "trace": {""}}, Data: []byte(body)}
			compact, spans, err := natsEvent(context.Background(), record, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(spans) != 0 || !bytes.HasPrefix(compact, []byte(`{"subject":"orders.created","headers":{"Trace":["a","b"],"trace":[""],"z":[]},"payload":`)) || compact[len(compact)-1] != '\n' {
				t.Fatalf("bad envelope: %q", compact)
			}
			pretty, spans, err := natsEvent(context.Background(), record, true, true)
			if err != nil || len(spans) == 0 || !bytes.HasPrefix(pretty, []byte("{\n  \"subject\": \"orders.created\",\n  \"headers\": {")) {
				t.Fatalf("pretty: %q %v", pretty, err)
			}
			for _, span := range spans {
				if span.Start < 0 || span.End > len(pretty) || span.Start >= span.End {
					t.Fatal(span)
				}
			}
		})
	}
	got, _, err := natsEvent(context.Background(), natsinspect.Record{Subject: "x", Data: []byte(`null`)}, true, false)
	want := "{\n  \"subject\": \"x\",\n  \"headers\": {},\n  \"payload\": null\n}\n"
	if err != nil || string(got) != want {
		t.Fatalf("pretty = %q %v", got, err)
	}
}

func TestNATSEventInvalidAndBudgets(t *testing.T) {
	for _, body := range []string{"", " ", "{", `{} {}`, `null]`, `0 trailing`, strings.Repeat("[", 1001) + "0" + strings.Repeat("]", 1001)} {
		if data, _, err := natsEvent(context.Background(), natsinspect.Record{Data: []byte(body)}, false, false); err == nil || len(data) != 0 {
			t.Fatalf("accepted invalid value %q", body)
		}
	}
	base := natsinspect.Record{Subject: "x", Data: []byte(`null`)}
	for _, which := range []string{"payload", "headers", "subject", "reply", "depth"} {
		t.Run(which, func(t *testing.T) {
			r := base
			switch which {
			case "payload":
				r.Data = []byte(`"` + strings.Repeat("x", (10<<20)-2) + `"`)
			case "headers":
				r.HeaderBytes = 64 << 10
			case "subject":
				r.Subject = strings.Repeat("x", 4096)
			case "reply":
				r.Reply = strings.Repeat("x", 4096)
			case "depth":
				r.Data = []byte(strings.Repeat("[", 1000) + "0" + strings.Repeat("]", 1000))
			}
			if _, _, err := natsEvent(context.Background(), r, false, false); err != nil {
				t.Fatal("at limit:", err)
			}
			switch which {
			case "payload":
				r.Data = append(r.Data, ' ')
			case "headers":
				r.HeaderBytes++
			case "subject":
				r.Subject += "x"
			case "reply":
				r.Reply += "x"
			case "depth":
				r.Data = append(append([]byte("["), r.Data...), ']')
			}
			if _, _, err := natsEvent(context.Background(), r, false, false); err == nil {
				t.Fatal("one over accepted")
			}
		})
	}
	// Escaping expands a valid sub-10-MiB input past the serialized cap.
	r := base
	r.Data = []byte(`"` + strings.Repeat("<", 3<<20) + `"`)
	if data, _, err := natsEvent(context.Background(), r, false, false); err == nil || data != nil {
		t.Fatal("escaped output limit not enforced")
	}
	// Layout alone can exceed the output cap; compact stays small.
	r.Data = []byte(strings.Repeat("[", 999) + strings.Repeat("0,", 9000) + "0" + strings.Repeat("]", 999))
	if _, _, err := natsEvent(context.Background(), r, false, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := natsEvent(context.Background(), r, true, false); err == nil {
		t.Fatal("pretty output limit not enforced")
	}
	boundary := base
	boundary.Data = []byte(`""`)
	empty, _, err := natsEvent(context.Background(), boundary, false, false)
	if err != nil {
		t.Fatal(err)
	}
	remaining := natsEventLimit - len(empty)
	text := strings.Repeat("<", remaining/6) + strings.Repeat("x", remaining%6)
	boundary.Data = []byte(`"` + text + `"`)
	atLimit, _, err := natsEvent(context.Background(), boundary, false, false)
	if err != nil || len(atLimit) != natsEventLimit {
		t.Fatal("serialized event at limit", len(atLimit), err)
	}
	boundary.Data = []byte(`"` + text + `x"`)
	if data, _, err := natsEvent(context.Background(), boundary, false, false); err == nil || data != nil {
		t.Fatal("serialized event one over accepted")
	}
	b := &eventBuffer{ctx: context.Background()}
	if _, err := b.Write(make([]byte, natsEventLimit-1)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteString("\n"); err != nil {
		t.Fatal("at output limit:", err)
	}
	if _, err := b.WriteString("x"); err == nil || b.Len() != natsEventLimit {
		t.Fatal("output grew beyond limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := natsEvent(ctx, base, false, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type natsFake struct {
	records       []natsinspect.Record
	next          func(context.Context) (natsinspect.Record, error)
	failure       error
	calls, closed int
	close         func()
}

func (f *natsFake) Next(ctx context.Context) (natsinspect.Record, error) {
	f.calls++
	if f.next != nil {
		return f.next(ctx)
	}
	if len(f.records) == 0 {
		<-ctx.Done()
		return natsinspect.Record{}, ctx.Err()
	}
	r := f.records[0]
	f.records = f.records[1:]
	return r, nil
}
func (f *natsFake) Err() error { return f.failure }
func (f *natsFake) Close() {
	f.closed++
	if f.close != nil {
		f.close()
	}
}

func fakeNATSRun(ctx context.Context, f *natsFake, n natsOptions, w io.Writer, logs *bytes.Buffer) error {
	return runNATS(RunOptions{Ctx: ctx, Stdout: w, Logger: logx.New(logs)}, n, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
		return f, nil
	})
}

func TestNATSSessionOutcomes(t *testing.T) {
	good := natsinspect.Record{Subject: "x", Data: []byte(`1`)}
	for _, tc := range []struct {
		name    string
		records []natsinspect.Record
		count   uint64
		want    string
		fail    bool
		calls   int
	}{
		{"count-no-drain", []natsinspect.Record{good, good, good}, 2, "reason=count; admitted=2 emitted=2 unfinished_event=false", false, 2},
		{"quiet", nil, 0, "reason=duration; admitted=0 emitted=0 unfinished_event=false", false, 1},
		{"valid-then-malformed", []natsinspect.Record{good, {Data: []byte(`{`)}, good}, 0, "admitted=2 emitted=1", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &natsFake{records: tc.records}
			var out, logs bytes.Buffer
			err := fakeNATSRun(context.Background(), f, natsOptions{duration: 20 * time.Millisecond, count: tc.count}, &out, &logs)
			if (err != nil) != tc.fail || f.calls != tc.calls || f.closed != 1 || !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("err=%v calls=%d closed=%d logs=%s", err, f.calls, f.closed, &logs)
			}
			if tc.name == "valid-then-malformed" && strings.Count(out.String(), "\n") != 1 {
				t.Fatal(out.String())
			}
		})
	}
	t.Run("status503-admitted", func(t *testing.T) {
		f := &natsFake{next: func(context.Context) (natsinspect.Record, error) {
			return natsinspect.Record{}, natsinspect.ErrEmptyPayload
		}}
		var logs bytes.Buffer
		if err := fakeNATSRun(context.Background(), f, natsOptions{duration: time.Second}, io.Discard, &logs); err == nil || !strings.Contains(logs.String(), "decode:") || !strings.Contains(logs.String(), "admitted=1 emitted=0") {
			t.Fatal(err, logs.String())
		}
	})
	t.Run("cancellation-safe-in-process", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		f := &natsFake{next: func(context.Context) (natsinspect.Record, error) {
			cancel()
			return natsinspect.Record{}, context.Canceled
		}}
		var logs bytes.Buffer
		err := fakeNATSRun(ctx, f, natsOptions{duration: time.Hour}, io.Discard, &logs)
		var cancelled *natsCancelled
		if !errors.As(err, &cancelled) {
			t.Fatal(err)
		}
	})
	t.Run("short-write", func(t *testing.T) {
		f := &natsFake{records: []natsinspect.Record{good}}
		var logs bytes.Buffer
		err := fakeNATSRun(context.Background(), f, natsOptions{duration: time.Second, count: 1}, shortNATSWriter{}, &logs)
		if err == nil || !strings.Contains(logs.String(), "final record may be incomplete") || !strings.Contains(logs.String(), "emitted=0") {
			t.Fatal(err, logs.String())
		}
	})
	t.Run("fatal-before-deadline", func(t *testing.T) {
		stop := newNATSStop(context.Background(), nil)
		defer stop.finish()
		stop.failure("decode: first")
		stop.request("duration", "", 0)
		stop.failure("write: later")
		reason, msg, code := stop.result()
		if reason != "failure" || msg != "decode: first" || code != 1 {
			t.Fatal(reason, msg, code)
		}
	})
	t.Run("deadline-before-later-processing-error", func(t *testing.T) {
		stop := newNATSStop(context.Background(), nil)
		defer stop.finish()
		stop.request("duration", "", 0)
		stop.failure("decode: found after stop")
		reason, _, code := stop.result()
		if reason != "duration" || code != 0 {
			t.Fatal(reason, code)
		}
	})
	t.Run("synchronous-error-before-close", func(t *testing.T) {
		stop := newNATSStop(context.Background(), nil)
		defer stop.finish()
		stop.request("duration", "", 0)
		stop.incomplete("delivery: already detected")
		_, msg, code := stop.result()
		if msg != "delivery: already detected" || code != 1 {
			t.Fatal(msg, code)
		}
	})
}

type shortNATSWriter struct{}

func (shortNATSWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestNATSExpiryBeforeEmission(t *testing.T) {
	f := &natsFake{records: []natsinspect.Record{{Data: []byte(`null`)}}}
	var logs, out bytes.Buffer
	err := runNATSWithRenderer(RunOptions{Ctx: context.Background(), Stdout: &out, Logger: logx.New(&logs)}, natsOptions{duration: 20 * time.Millisecond}, nil,
		func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			return f, nil
		},
		func(ctx context.Context, _ natsinspect.Record, _, _ bool) ([]byte, []display.Span, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		})
	if err != nil || out.Len() != 0 || !strings.Contains(logs.String(), "reason=duration; admitted=1 emitted=0 unfinished_event=true") {
		t.Fatal(err, out.String(), logs.String())
	}
}

func TestNATSExpiryDuringEmission(t *testing.T) {
	f := &natsFake{records: []natsinspect.Record{{Data: []byte(`null`)}}}
	var logs bytes.Buffer
	stop := newNATSStop(context.Background(), nil)
	defer stop.finish()
	w := &expiryNATSWriter{stop: stop}
	err := runNATS(RunOptions{Ctx: context.Background(), Stdout: w, Logger: logx.New(&logs)}, natsOptions{duration: time.Second}, stop,
		func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			return f, nil
		})
	if err == nil || !strings.Contains(logs.String(), "write:") || !strings.Contains(logs.String(), "admitted=1 emitted=0 unfinished_event=true") {
		t.Fatal(err, logs.String())
	}
}

type expiryNATSWriter struct{ stop *natsStop }

func (w *expiryNATSWriter) Write(p []byte) (int, error) {
	w.stop.request("duration", "", 0)
	return len(p) / 2, context.DeadlineExceeded
}

func TestNATSStopWatchdog(t *testing.T) {
	exited := make(chan int, 1)
	stop := newNATSStop(context.Background(), func(code int) { exited <- code })
	defer stop.finish()
	start := time.Now()
	stop.request("cancel", "cancel", 130)
	stop.request("duration", "", 0)
	select {
	case code := <-exited:
		if code != 130 || time.Since(start) > 2*time.Second {
			t.Fatal(code, time.Since(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire")
	}
}

func TestNATSStopConcurrent(t *testing.T) {
	stop := newNATSStop(context.Background(), nil)
	defer stop.finish()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop.request("duration", "", 0)
			stop.incomplete("delivery: failure")
			stop.result()
		}()
	}
	wg.Wait()
	_, _, code := stop.result()
	if code != 1 {
		t.Fatal(code)
	}
}

func TestNATSModeValidation(t *testing.T) {
	base := []string{"--nats", "orders.>", "--server", "nats://127.0.0.1:1", "--duration", "1s"}
	for _, args := range [][]string{
		{"--server", "nats://127.0.0.1:1"}, {"--nats", "x"},
		append(append([]string{}, base...), "file.json"),
		append(append([]string{}, base...), "--count", "0"),
		append(append([]string{}, base...), "--query", "."),
		append(append([]string{}, base...), "--nats-output", "bad"),
		{"--nats", "a..b", "--server", "nats://127.0.0.1:1", "--duration", "1s"},
		{"--nats", "a.>.b", "--server", "nats://127.0.0.1:1", "--duration", "1s"},
		{"--nats", "x", "--server", "nats://private:secret@localhost:4222", "--duration", "1s"},
	} {
		var logs, out bytes.Buffer
		code := Execute(RunOptions{Args: args, Stdin: panicReader{}, Stdout: &out, Stderr: &logs})
		if code != 1 || out.Len() != 0 || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), "secret") {
			t.Fatalf("%v: code=%d logs=%s", args, code, &logs)
		}
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("NATS must not read stdin") }
