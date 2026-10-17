package natsinspect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestHistoryTimeBounds(t *testing.T) {
	for _, input := range []string{"1677-09-21T00:12:43.145224192Z", "2262-04-11T23:47:16.854775807Z", "1677-09-21T01:12:43.145224192+01:00", "2262-04-11T22:47:16.854775807-01:00", "2026-10-17T00:00:00Z"} {
		if _, err := ParseHistoryTime(input); err != nil {
			t.Errorf("%s: %v", input, err)
		}
	}
	for _, input := range []string{"1677-09-21T00:12:43.145224191Z", "2262-04-11T23:47:16.854775808Z", "1677-09-21T01:12:43.145224191+01:00", "2262-04-11T22:47:16.854775808-01:00", "2500-01-01T00:00:00Z", "2026-10-17T00:00:00", "2026-10-17T00:00:00.1234567890Z", "2026-10-17T00:00:00,1Z", "2026-10-17T00:00:00+24:00", "2026-10-17T00:00:00+01:60", "2026-10-17T0:00:00Z"} {
		if _, err := ParseHistoryTime(input); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

const infoJSON = `{"type":"io.nats.jetstream.api.v1.stream_info_response","config":{"name":"ORDERS","metadata":{"_nats.ver":"2.15.0"}},"created":"2026-01-01T00:00:00Z","state":{"messages":3,"bytes":100,"first_seq":1,"first_ts":"2026-01-01T00:00:01Z","last_seq":9,"last_ts":"2026-01-01T00:00:09Z","consumer_count":0}}`
const noMessageJSON = `{"type":"io.nats.jetstream.api.v1.stream_msg_get_response","error":{"code":404,"err_code":10037,"description":"no message found"}}`

func fakeHistory(request historyRequest) *history {
	return &history{client: &client{closing: true}, request: request, options: HistoryOptions{Stream: "ORDERS", Subject: "orders.>", StartSeq: 1}, cursor: 1, upper: 9, first: true, created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func apiReply(text string) *nats.Msg {
	return &nats.Msg{Subject: "_INBOX.synthetic.reply", Data: []byte(text)}
}
func getJSON(seq uint64, subject, timestamp string, data, header []byte) string {
	msg := map[string]any{"subject": subject, "seq": seq, "time": timestamp}
	if data != nil {
		msg["data"] = base64.StdEncoding.EncodeToString(data)
	}
	if header != nil {
		msg["hdrs"] = base64.StdEncoding.EncodeToString(header)
	}
	out, _ := json.Marshal(map[string]any{"type": "io.nats.jetstream.api.v1.stream_msg_get_response", "message": msg})
	return string(out)
}
func TestHistoryProtocolInfo(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		good       bool
	}{
		{"valid", infoJSON, true},
		{"additive", strings.Replace(infoJSON, `"config":`, `"new":{"nested":[1,null]},"config":`, 1), true},
		{"cluster", strings.Replace(infoJSON, `"config":`, `"cluster":{"name":"C","leader":"s1"},"config":`, 1), true},
		{"leaderless", strings.Replace(infoJSON, `"config":`, `"cluster":{"name":"C"},"config":`, 1), false},
		{"wrong-stream", strings.Replace(infoJSON, "ORDERS", "OTHER", 1), false},
		{"old-leader", strings.Replace(infoJSON, "2.15.0", "2.14.0", 1), false},
		{"future", strings.Replace(infoJSON, "2.15.0", "2.16.0", 1), false},
		{"prerelease", strings.Replace(infoJSON, "2.15.0", "2.15.1-rc.1", 1), false},
		{"metadata-missing", strings.Replace(infoJSON, `"_nats.ver":"2.15.0"`, `"other":"2.15.0"`, 1), false},
		{"duplicate", strings.Replace(infoJSON, `"last_seq":9`, `"last_seq":9,"last_seq":9`, 1), false},
		{"duplicate-metadata", strings.Replace(infoJSON, `"_nats.ver":"2.15.0"`, `"_nats.ver":"2.15.0","_nats.ver":"2.15.0"`, 1), false},
		{"null-state", strings.Replace(infoJSON, `"messages":3`, `"messages":null`, 1), false},
		{"float-seq", strings.Replace(infoJSON, `"last_seq":9`, `"last_seq":9.0`, 1), false},
		{"missing-seq", strings.Replace(infoJSON, `"last_seq":9,`, ``, 1), false},
		{"trailing", infoJSON + ` {}`, false},
		{"wrong-type", strings.Replace(infoJSON, "stream_info_response", "stream_msg_get_response", 1), false},
		{"success-error", strings.Replace(infoJSON, `"config":`, `"error":{"code":404,"err_code":10059,"description":"gone"},"config":`, 1), false},
		{"impossible-count", strings.Replace(infoJSON, `"messages":3`, `"messages":30`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fakeHistory(func(ctx context.Context, subject string, body []byte) (*nats.Msg, error) {
				if subject != "$JS.API.STREAM.INFO.ORDERS" || string(body) != "{}" {
					t.Fatal(subject, string(body))
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 5*time.Second {
					t.Fatal("missing request deadline")
				}
				return apiReply(tc.data), nil
			})
			_, err := h.info(context.Background())
			if (err == nil) != tc.good {
				t.Fatalf("good=%t err=%v", tc.good, err)
			}
		})
	}
	for _, state := range []string{
		`{"messages":0,"bytes":0,"first_seq":0,"last_seq":0,"first_ts":"0001-01-01T00:00:00Z","last_ts":"0001-01-01T00:00:00Z","consumer_count":0}`,
		`{"messages":0,"bytes":0,"first_seq":10,"last_seq":9,"first_ts":"0001-01-01T00:00:00Z","last_ts":"2026-01-01T00:00:09Z","consumer_count":0}`,
	} {
		o, _ := parseProtocolObject([]byte(infoJSON))
		o["state"] = json.RawMessage(state)
		data, _ := json.Marshal(o)
		h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) { return apiReply(string(data)), nil })
		if _, err := h.info(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryProtocolGet(t *testing.T) {
	valid := getJSON(3, "orders.x", "2026-01-01T00:00:03Z", []byte(`{}`), nil)
	for _, tc := range []struct {
		name, data string
		exhausted  bool
	}{
		{"duplicate", strings.Replace(valid, `"seq":3`, `"seq":3,"seq":3`, 1), false},
		{"float", strings.Replace(valid, `"seq":3`, `"seq":3.0`, 1), false},
		{"overflow", strings.Replace(valid, `"seq":3`, `"seq":18446744073709551616`, 1), false},
		{"zero", strings.Replace(valid, `"seq":3`, `"seq":0`, 1), false},
		{"wrong-subject", strings.Replace(valid, "orders.x", "other.x", 1), false},
		{"wildcard-subject", strings.Replace(valid, "orders.x", "orders.*", 1), false},
		{"wrong-time", strings.Replace(valid, "2026-01-01T00:00:03Z", "2500-01-01T00:00:00Z", 1), false},
		{"null-data", strings.Replace(valid, `"data":"e30="`, `"data":null`, 1), false},
		{"bad-base64", strings.Replace(valid, "e30=", "???=", 1), false},
		{"missing-time", strings.Replace(valid, `"time":`, `"other":`, 1), false},
		{"trailing", valid + `false`, false},
		{"wrong-type", strings.Replace(valid, "stream_msg_get_response", "stream_info_response", 1), false},
		{"no-message", noMessageJSON, true},
		{"missing-stream", strings.Replace(noMessageJSON, "10037", "10059", 1), false},
		{"wrong-code", strings.Replace(noMessageJSON, "404", "500", 1), false},
		{"success-error", strings.Replace(valid, `"message":`, `"error":{"code":404,"err_code":10037,"description":"absent"},"message":`, 1), false},
		{"missing-code", strings.Replace(noMessageJSON, `"code":404,`, ``, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infoCalls := 0
			h := fakeHistory(func(_ context.Context, subject string, _ []byte) (*nats.Msg, error) {
				if strings.Contains(subject, ".INFO.") {
					infoCalls++
					return apiReply(infoJSON), nil
				}
				return apiReply(tc.data), nil
			})
			_, err := h.Next(context.Background())
			if err == nil || errors.Is(err, ErrExhausted) != tc.exhausted {
				t.Fatal(err)
			}
			if tc.exhausted && infoCalls != 1 {
				t.Fatal("no identity recheck")
			}
		})
	}
}

func TestHistoryCursorAndIdentity(t *testing.T) {
	var calls []string
	h := fakeHistory(func(_ context.Context, subject string, body []byte) (*nats.Msg, error) {
		calls = append(calls, subject+" "+string(body))
		if strings.Contains(subject, ".INFO.") {
			return apiReply(infoJSON), nil
		}
		if len(calls) == 1 {
			return apiReply(getJSON(3, "orders.x", "2026-01-01T00:00:03Z", []byte(`{}`), nil)), nil
		}
		return apiReply(getJSON(9, "orders.x", "2026-01-01T00:00:09Z", nil, []byte("NATS/1.0\r\nStatus: 503\r\nTrace: a\r\nTrace: b\r\n\r\n"))), nil
	})
	h.options.StartTime, _ = ParseHistoryTime("2026-01-01T00:00:02Z")
	h.options.StartSeq = 0
	h.cursor = 0
	for _, want := range []uint64{3, 9} {
		r, err := h.Next(context.Background())
		if err != nil || r.History.Sequence != want {
			t.Fatal(r, err)
		}
		if want == 9 && (len(r.Data) != 0 || len(r.Headers["Trace"]) != 2 || r.Headers["Status"][0] != "503") {
			t.Fatal(r)
		}
	}
	if _, err := h.Next(context.Background()); !errors.Is(err, ErrExhausted) {
		t.Fatal(err)
	}
	if len(calls) != 3 || strings.Contains(calls[0], `"seq"`) || !strings.Contains(calls[0], `"start_time":"2026-01-01T00:00:02Z"`) || !strings.Contains(calls[1], `"seq":4`) || !strings.Contains(calls[2], ".INFO.") {
		t.Fatal(calls)
	}
	for _, result := range []string{strings.Replace(infoJSON, "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", 1), strings.Replace(noMessageJSON, "stream_msg_get", "stream_info", 1), "{}"} {
		h.request = func(context.Context, string, []byte) (*nats.Msg, error) { return apiReply(result), nil }
		if _, err := h.Next(context.Background()); err == nil || errors.Is(err, ErrExhausted) {
			t.Fatal(err)
		}
	}
}

func TestHistoryRangeAndMonotonicity(t *testing.T) {
	for _, seq := range []uint64{1, 3, 10, ^uint64(0)} {
		calls := 0
		h := fakeHistory(func(_ context.Context, subject string, _ []byte) (*nats.Msg, error) {
			calls++
			if strings.Contains(subject, ".INFO.") {
				return apiReply(infoJSON), nil
			}
			return apiReply(getJSON(seq, "orders.x", "2026-01-01T00:00:03Z", nil, nil)), nil
		})
		h.cursor = 3
		r, err := h.Next(context.Background())
		switch {
		case seq < 3:
			if err == nil {
				t.Fatal("backwards accepted")
			}
		case seq > 9:
			if !errors.Is(err, ErrExhausted) || calls != 2 {
				t.Fatal(err, calls)
			}
		default:
			if err != nil || r.History.Sequence != 3 || h.cursor != 4 {
				t.Fatal(r, err)
			}
		}
	}
	h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) {
		return apiReply(getJSON(^uint64(0), "orders.x", "2026-01-01T00:00:03Z", nil, nil)), nil
	})
	h.upper = ^uint64(0)
	h.cursor = h.upper
	if _, err := h.Next(context.Background()); err != nil || !h.boundary || h.cursor != h.upper {
		t.Fatal(err, h)
	}
}

func TestHistoryResponseAndBinaryLimits(t *testing.T) {
	for _, limit := range []int{historyInfoLimit, historyGetLimit} {
		for _, extra := range []int{0, 1} {
			data := `{"padding":"` + strings.Repeat("x", limit-len(`{"padding":""}`)+extra) + `"}`
			h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) { return apiReply(data), nil })
			_, err := h.response(context.Background(), "INFO", nil, limit)
			if (err != nil) != (extra == 1) {
				t.Fatal(limit, extra, err)
			}
		}
	}
	for _, limit := range []int{historyHeaderLimit, historyPayloadLimit} {
		for _, extra := range []int{0, 1} {
			data, _ := json.Marshal(base64.StdEncoding.EncodeToString(make([]byte, limit+extra)))
			decoded, err := (protocolObject{"data": data}).binary("data", limit)
			if (err != nil) != (extra == 1) || (err == nil && len(decoded) != limit) {
				t.Fatal(limit, extra, err)
			}
		}
	}
	for _, bad := range []string{"\"YQ==\\n\"", "\"YR==\"", "123", "null"} {
		if _, err := (protocolObject{"data": json.RawMessage(bad)}).binary("data", 10); err == nil {
			t.Fatal(bad)
		}
	}
	for _, m := range []*nats.Msg{{Subject: strings.Repeat("s", 4097), Data: []byte("{}")}, {Reply: strings.Repeat("r", 4097), Data: []byte("{}")}, {Header: nats.Header{"Long": {strings.Repeat("a", historyHeaderLimit)}}, Data: []byte("{}")}} {
		h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) { return m, nil })
		if _, err := h.response(context.Background(), "INFO", nil, historyInfoLimit); err == nil {
			t.Fatal("oversized routing/header accepted")
		}
	}
}

func TestHistoryHeaderFraming(t *testing.T) {
	for _, header := range []string{"NATS/1.0\r\n\r\n", "NATS/1.0 503 No Responders\r\nTrace: a\r\nTrace: b\r\n\r\n", "NATS/1.0\r\nTrace: a\x00b\r\n\r\n"} {
		if !validStoredHeader([]byte(header)) {
			t.Fatal(header)
		}
	}
	for _, header := range []string{"NATS/1.0\n\n", "NATS/1.0\r\n", "NATS/1.0\r\n\r\ngarbage", "NATS/1.0\r\n\r\n\r\n", "NATS/1.0\r\n: empty\r\n\r\n", "NATS/1.0oops\r\n\r\n", "NATS/1.0\r\n folded\r\n\r\n", "NATS/1.0 x00\r\n\r\n"} {
		if validStoredHeader([]byte(header)) {
			t.Fatal(header)
		}
	}
}

func TestHistoryRequestErrorPrecedence(t *testing.T) {
	for _, err := range []error{nats.ErrNoResponders, nats.ErrAuthorization, fmt.Errorf("synthetic private error"), context.DeadlineExceeded} {
		h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) { return nil, err })
		if _, got := h.Next(context.Background()); got == nil || errors.Is(got, ErrExhausted) || strings.Contains(got.Error(), "private") {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := fakeHistory(func(context.Context, string, []byte) (*nats.Msg, error) {
		cancel()
		return apiReply(`{"type":false}`), nil
	})
	if _, err := h.Next(ctx); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("protocol failure hidden by cancellation", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	h.request = func(context.Context, string, []byte) (*nats.Msg, error) { cancel(); return nil, context.Canceled }
	if _, err := h.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestHistoryRequestDeadlineAndLowerTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	h := fakeHistory(func(child context.Context, _ string, _ []byte) (*nats.Msg, error) {
		deadline, ok := child.Deadline()
		if !ok || !deadline.Equal(parentDeadline) {
			t.Fatal("request not capped by total duration", deadline)
		}
		return apiReply(getJSON(3, "orders.x", "2026-01-01T00:00:01Z", nil, nil)), nil
	})
	h.options.StartTime, _ = ParseHistoryTime("2026-01-01T00:00:02Z")
	h.options.StartSeq, h.cursor = 0, 0
	if _, err := h.Next(ctx); err == nil || errors.Is(err, ErrExhausted) {
		t.Fatal("time lower bound not enforced", err)
	}
}

func TestHistorySubjectMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern, subject string
		want             bool
	}{
		{"orders.>", "orders", false}, {"orders.>", "orders.x.y", true},
		{"orders.*", "orders.x.y", false}, {"orders.*", "orders.x", true},
		{">", "orders", true}, {"orders.x", "orders.x", true},
		{"orders.x", "orders.y", false}, {"*.x", "orders.x", true},
	} {
		if got := matchesSubject(tc.pattern, tc.subject); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
