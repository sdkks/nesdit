package run

import (
	"context"
	"sync"
	"time"
)

// natsStop owns one stop decision and one cleanup allowance. It never writes
// diagnostics or waits for a client lock. Only Run supplies a process exit hook.
type natsStop struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	reason, message string
	code            int
	timer           *time.Timer
	exit            func(int)
	finished        bool
	unwatch         func() bool
}

func newNATSStop(parent context.Context, exit func(int)) *natsStop {
	ctx, cancel := context.WithCancel(parent)
	s := &natsStop{ctx: ctx, cancel: cancel, exit: exit}
	s.unwatch = context.AfterFunc(parent, func() { s.request("cancel", "cancel: observation cancelled", 130) })
	if parent.Err() != nil {
		s.request("cancel", "cancel: observation cancelled", 130)
	}
	return s
}

func (s *natsStop) request(reason, message string, code int) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if s.reason == "" {
		s.reason, s.message, s.code = reason, message, code
		if s.exit != nil {
			s.timer = time.AfterFunc(time.Second, func() {
				s.mu.Lock()
				code, finished := s.code, s.finished
				s.mu.Unlock()
				if !finished {
					if code != 130 {
						code = 1
					}
					s.exit(code)
				}
			})
		}
	}
	s.mu.Unlock()
	s.cancel()
}

func (s *natsStop) failure(message string) {
	s.request("failure", message, 1)
}

// Incomplete emission and synchronous adapter errors captured before deliberate
// teardown cannot be reported as normal duration/count completion.
func (s *natsStop) incomplete(message string) {
	s.mu.Lock()
	if !s.finished && (s.reason == "duration" || s.reason == "count") {
		s.reason, s.message, s.code = "failure", message, 1
	}
	s.mu.Unlock()
	s.request("failure", message, 1)
}

func (s *natsStop) result() (string, string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason, s.message, s.code
}

func (s *natsStop) finish() {
	s.mu.Lock()
	s.finished = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.unwatch()
	s.cancel()
}

// The holder is invocation-local. Execute never constructs one.
type natsProcess struct {
	stop        *natsStop
	signalsDone func()
	exit        func(int)
}

func (p *natsProcess) start(parent context.Context) {
	if p.stop != nil {
		return
	}
	p.stop = newNATSStop(parent, p.exit)
	p.signalsDone = natsSignals(func() { p.stop.request("cancel", "cancel: observation interrupted", 130) })
}

func (p *natsProcess) finish() {
	if p.stop != nil {
		p.stop.finish()
		p.signalsDone()
	}
}
