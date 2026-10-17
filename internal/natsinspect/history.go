package natsinspect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nats-io/nats.go"
)

const (
	historyInfoLimit    = 1 << 20
	historyGetLimit     = 16 << 20
	historyPayloadLimit = 10 << 20
	historyHeaderLimit  = 64 << 10
)

// ErrExhausted means the bounded retained range ended, not complete history.
var ErrExhausted = errors.New("history: retained range exhausted")

// HistoryMetadata is validated broker provenance, separate from stored content.
type HistoryMetadata struct {
	Stream     string
	Sequence   uint64
	StoredTime time.Time
}

// HistoryOptions is an explicit, bounded standard leader read selection.
type HistoryOptions struct {
	Stream, Subject  string
	StartSeq, EndSeq uint64
	StartTime        time.Time
}

var historyTimeSyntax = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-][0-9]{2}:[0-9]{2})$`)
var historyVersionSyntax = regexp.MustCompile(`^2\.15\.(0|[1-9]\d*)$`)

// ParseHistoryTime checks bounds before any signed nanosecond conversion.
func ParseHistoryTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	lo := time.Date(1677, 9, 21, 0, 12, 43, 145224192, time.UTC)
	hi := time.Date(2262, 4, 11, 23, 47, 16, 854775807, time.UTC)
	if err != nil || !historyTimeSyntax.MatchString(s) || t.Before(lo) || t.After(hi) {
		return time.Time{}, errors.New("history: time must be timezone-qualified RFC3339 with at most nine fractional digits, within 1677-09-21T00:12:43.145224192Z through 2262-04-11T23:47:16.854775807Z")
	}
	// time.Parse accepts offset hours/minutes at their boundary; RFC3339 does not.
	if s[len(s)-1] != 'Z' && (s[len(s)-5:len(s)-3] > "23" || s[len(s)-2:] > "59") {
		return time.Time{}, errors.New("history: invalid RFC3339 timezone offset")
	}
	return t.UTC(), nil
}

func validSubject(s string, pattern bool) bool {
	if s == "" || len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	tokens := strings.Split(s, ".")
	for i, token := range tokens {
		if token == "" {
			return false
		}
		if strings.ContainsAny(token, "*>") && (!pattern || (token != "*" && (token != ">" || i != len(tokens)-1))) {
			return false
		}
	}
	return true
}

// Validate rejects unsafe API tokens and unbounded or inconsistent starts.
func (o HistoryOptions) Validate() error {
	if !validSubject(o.Stream, false) || strings.ContainsAny(o.Stream, ".\\/") || len("$JS.API.STREAM.MSG.GET."+o.Stream) > 4096 {
		return errors.New("--nats-history requires one safe stream token without dots, wildcards, path separators or whitespace")
	}
	if !validSubject(o.Subject, true) {
		return errors.New("--subject requires a valid NATS subject pattern, at most 4 KiB")
	}
	if (o.StartSeq == 0) == o.StartTime.IsZero() {
		return errors.New("history requires exactly one positive --start-seq or --start-time")
	}
	if o.StartSeq > 0 && o.EndSeq > 0 && o.EndSeq < o.StartSeq {
		return errors.New("--end-seq must not precede --start-seq")
	}
	if !o.StartTime.IsZero() {
		if _, err := ParseHistoryTime(o.StartTime.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

type historyRequest func(context.Context, string, []byte) (*nats.Msg, error)
type history struct {
	*client
	request         historyRequest
	options         HistoryOptions
	created         time.Time
	upper, cursor   uint64
	first, boundary bool
}

// OpenHistory returns ready only after a validated, supported initial INFO.
func OpenHistory(ctx context.Context, server string, options HistoryOptions, security Security, onFailure func(error)) (Session, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	c, err := connectConfigured(ctx, server, onFailure, security, nil)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), "subscription was not ready", "history was not ready"))
	}
	if !historyVersionSyntax.MatchString(c.nc.ConnectedServerVersion()) {
		c.Close()
		return nil, errors.New("history: requires a stable NATS 2.15.x server and homogeneous same-series cluster")
	}
	h := &history{client: c, request: c.nc.RequestWithContext, options: options, cursor: options.StartSeq, first: true}
	info, err := h.info(ctx)
	if err == nil {
		err = c.Err()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	h.created, h.upper = info.created, info.last
	if options.EndSeq > 0 && options.EndSeq < h.upper {
		h.upper = options.EndSeq
	}
	h.boundary = info.messages == 0 || h.upper == 0 || h.cursor > h.upper
	return h, nil
}

func (h *history) response(ctx context.Context, operation string, body []byte, limit int) (protocolObject, error) {
	if err := h.Err(); err != nil {
		return nil, err
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m, err := h.request(child, "$JS.API.STREAM."+operation+"."+h.options.Stream, body)
	if err != nil {
		if failure := h.Err(); failure != nil {
			return nil, failure
		}
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrConnectionClosed)) {
			return nil, ctx.Err()
		}
		return nil, errors.New("history: leader request failed; check INFO/GET and reply-inbox permissions, server availability and request timeout")
	}
	if m == nil || len(m.Data) > limit || len(m.Subject) > 4096 || len(m.Reply) > 4096 || m.Size()-len(m.Data)-len(m.Subject)-len(m.Reply) > historyHeaderLimit {
		return nil, errors.New("history: API response exceeds payload or metadata limit")
	}
	return parseProtocolObject(m.Data)
}

// A small DTO reader preserves integers and rejects duplicates, nulls and
// trailing JSON. Unknown additive fields remain opaque, rather than becoming
// an unbounded recursive application-object conversion.
type protocolObject map[string]json.RawMessage

func parseProtocolObject(data []byte) (protocolObject, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("history: API JSON must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("history: expected API object")
	}
	out := make(protocolObject)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, errors.New("history: malformed API object")
		}
		name, ok := key.(string)
		if !ok {
			return nil, errors.New("history: invalid API key")
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("history: duplicate API key")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, errors.New("history: malformed API value")
		}
		out[name] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, errors.New("history: malformed API object terminator")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("history: trailing API content")
	}
	return out, nil
}
func (o protocolObject) value(key string, dest any) error {
	raw, ok := o[key]
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, dest) != nil {
		return fmt.Errorf("history: missing or invalid API field %s", key)
	}
	return nil
}
func (o protocolObject) object(key string) (protocolObject, error) {
	return parseProtocolObject(o[key])
}
func (o protocolObject) envelope(kind string, success ...string) error {
	var typ string
	if err := o.value("type", &typ); err != nil {
		return err
	}
	if typ != "io.nats.jetstream.api.v1."+kind+"_response" {
		return errors.New("history: unexpected API response type")
	}
	if _, ok := o["error"]; !ok {
		return nil
	}
	for _, field := range success {
		if _, ok := o[field]; ok {
			return errors.New("history: simultaneous API success and error")
		}
	}
	api, err := o.object("error")
	if err != nil {
		return err
	}
	var code, number uint64
	var description string
	if err := api.value("code", &code); err != nil {
		return err
	}
	if err := api.value("err_code", &number); err != nil {
		return err
	}
	if err := api.value("description", &description); err != nil {
		return err
	}
	if kind == "stream_msg_get" && code == 404 && number == 10037 {
		return ErrExhausted
	}
	return fmt.Errorf("history: API failure code=%d err_code=%d", code, number)
}

type historyInfo struct {
	created        time.Time
	messages, last uint64
}

func (h *history) info(ctx context.Context) (historyInfo, error) {
	var info historyInfo
	o, err := h.response(ctx, "INFO", []byte("{}"), historyInfoLimit)
	if err != nil {
		return info, err
	}
	if err := o.envelope("stream_info", "config", "created", "state", "cluster"); err != nil {
		return info, err
	}
	config, err := o.object("config")
	if err != nil {
		return info, err
	}
	var name string
	if err := config.value("name", &name); err != nil {
		return info, err
	}
	if name != h.options.Stream {
		return info, errors.New("history: INFO stream identity mismatch")
	}
	metadata, err := config.object("metadata")
	if err != nil {
		return info, err
	}
	var version string
	if err := metadata.value("_nats.ver", &version); err != nil {
		return info, err
	}
	if !historyVersionSyntax.MatchString(version) {
		return info, errors.New("history: INFO responder must be stable NATS 2.15.x")
	}
	if _, present := o["cluster"]; present {
		cluster, err := o.object("cluster")
		if err != nil {
			return info, err
		}
		var leader string
		if err := cluster.value("leader", &leader); err != nil {
			return info, err
		}
		if strings.TrimSpace(leader) == "" {
			return info, errors.New("history: clustered INFO has no leader")
		}
	}
	var created string
	if err := o.value("created", &created); err != nil {
		return info, err
	}
	info.created, err = ParseHistoryTime(created)
	if err != nil {
		return info, err
	}
	state, err := o.object("state")
	if err != nil {
		return info, err
	}
	var first, size, consumers uint64
	for key, target := range map[string]*uint64{"messages": &info.messages, "last_seq": &info.last, "first_seq": &first, "bytes": &size, "consumer_count": &consumers} {
		if err := state.value(key, target); err != nil {
			return info, err
		}
	}
	for _, key := range []string{"first_ts", "last_ts"} {
		var text string
		if err := state.value(key, &text); err != nil {
			return info, err
		}
		t, parseErr := time.Parse(time.RFC3339Nano, text)
		if parseErr != nil || (info.messages > 0 && t.IsZero()) {
			return info, errors.New("history: invalid stream state time")
		}
		if !t.IsZero() {
			if _, err = ParseHistoryTime(text); err != nil {
				return info, err
			}
		}
	}
	// Empty stores can have (0,0), or first=last+1 after purge/deletion.
	// Last time may remain nonzero after purge; it is not an emptiness test.
	if info.messages > 0 && (first == 0 || first > info.last || info.messages-1 > info.last-first) {
		return info, errors.New("history: inconsistent retained stream range")
	}
	if info.messages == 0 && (size != 0 || (first != 0 && (info.last == ^uint64(0) || first != info.last+1))) {
		return info, errors.New("history: inconsistent empty stream state")
	}
	return info, nil
}

func (h *history) exhausted(ctx context.Context) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	info, err := h.info(ctx)
	if err != nil {
		return Record{}, err
	}
	if !info.created.Equal(h.created) {
		return Record{}, errors.New("history: stream was replaced during inspection")
	}
	if err := h.Err(); err != nil {
		return Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	return Record{}, ErrExhausted
}

func (h *history) Next(ctx context.Context) (Record, error) {
	if h.boundary {
		return h.exhausted(ctx)
	}
	request := struct {
		Seq       uint64     `json:"seq,omitempty"`
		StartTime *time.Time `json:"start_time,omitempty"`
		Subject   string     `json:"next_by_subj"`
	}{Seq: h.cursor, Subject: h.options.Subject}
	if h.first && !h.options.StartTime.IsZero() {
		request.StartTime = &h.options.StartTime
		request.Seq = 0
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Record{}, errors.New("history: invalid request")
	}
	o, err := h.response(ctx, "MSG.GET", body, historyGetLimit)
	if err != nil {
		return Record{}, err
	}
	if err = o.envelope("stream_msg_get", "message"); errors.Is(err, ErrExhausted) {
		return h.exhausted(ctx)
	}
	if err != nil {
		return Record{}, err
	}
	record, err := h.record(o)
	if err != nil {
		return Record{}, err
	}
	seq := record.History.Sequence
	if seq > h.upper {
		return h.exhausted(ctx)
	}
	h.first = false
	h.boundary = seq == h.upper
	if !h.boundary {
		h.cursor = seq + 1
	}
	return record, nil
}

func (h *history) record(o protocolObject) (Record, error) {
	var r Record
	message, err := o.object("message")
	if err != nil {
		return r, err
	}
	meta := &HistoryMetadata{Stream: h.options.Stream}
	var stored string
	if err := message.value("subject", &r.Subject); err != nil {
		return r, err
	}
	if err := message.value("seq", &meta.Sequence); err != nil {
		return r, err
	}
	if err := message.value("time", &stored); err != nil {
		return r, err
	}
	meta.StoredTime, err = ParseHistoryTime(stored)
	if err != nil {
		return r, err
	}
	if !validSubject(r.Subject, false) || !matchesSubject(h.options.Subject, r.Subject) || meta.Sequence == 0 || meta.Sequence < h.cursor || (!h.options.StartTime.IsZero() && meta.StoredTime.Before(h.options.StartTime)) {
		return r, errors.New("history: message violates requested subject, sequence or time bound")
	}
	if r.Data, err = message.binary("data", historyPayloadLimit); err != nil {
		return r, err
	}
	headers, err := message.binary("hdrs", historyHeaderLimit)
	if err != nil {
		return r, err
	}
	if len(headers) > 0 {
		if !validStoredHeader(headers) {
			return r, errors.New("history: invalid stored header framing")
		}
		r.Headers, err = nats.DecodeHeadersMsg(headers)
		if err != nil {
			return r, errors.New("history: invalid stored headers")
		}
	}
	r.HeaderBytes, r.History = len(headers), meta
	return r, nil
}
func (o protocolObject) binary(key string, limit int) ([]byte, error) {
	if _, ok := o[key]; !ok {
		return nil, nil
	}
	var text string
	if err := o.value(key, &text); err != nil {
		return nil, err
	}
	if len(text) > base64.StdEncoding.EncodedLen(limit) || strings.ContainsAny(text, "\r\n") {
		return nil, errors.New("history: encoded payload or headers exceed limit or contain invalid base64")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || len(data) > limit {
		return nil, errors.New("history: invalid or oversized base64 payload or headers")
	}
	return data, nil
}
func matchesSubject(pattern, subject string) bool {
	p, s := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, token := range p {
		if i >= len(s) {
			return false
		}
		if token == ">" {
			return true
		}
		if token != "*" && token != s[i] {
			return false
		}
	}
	return len(p) == len(s)
}
func validStoredHeader(data []byte) bool {
	if !bytes.HasSuffix(data, []byte("\r\n\r\n")) || bytes.Index(data, []byte("\r\n\r\n")) != len(data)-4 {
		return false
	}
	lines := strings.Split(string(data[:len(data)-4]), "\r\n")
	first := lines[0]
	if first != "NATS/1.0" {
		if !strings.HasPrefix(first, "NATS/1.0 ") {
			return false
		}
		status := strings.TrimPrefix(first, "NATS/1.0 ")
		if len(status) < 3 || status[0] < '0' || status[0] > '9' || status[1] < '0' || status[1] > '9' || status[2] < '0' || status[2] > '9' || (len(status) > 3 && status[3] != ' ') {
			return false
		}
	}
	for i, line := range lines {
		if strings.ContainsAny(line, "\r\n") {
			return false
		}
		if i == 0 {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if !ok || key == "" || strings.ContainsFunc(key, func(r rune) bool { return r <= 32 || r >= 127 }) {
			return false
		}
	}
	return true
}
