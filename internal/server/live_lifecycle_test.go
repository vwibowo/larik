package server

import (
	"context"
	"errors"
	"testing"
)

func TestIdleOperationInheritsContextAndPublishesBusyTransitions(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	parent, cancel := context.WithCancel(context.Background())
	_, ch, gap := l.bus.subscribe(0)
	defer l.bus.unsubscribe(ch)
	if gap {
		t.Fatal("unexpected replay gap")
	}
	if err := l.idleDo(parent, func(ctx context.Context) error {
		if !l.isBusy() {
			t.Error("idle operation did not mark session busy")
		}
		if err := l.idleDo(ctx, func(context.Context) error { return nil }); !errors.Is(err, errBusy) {
			t.Errorf("nested operation = %v, want busy", err)
		}
		cancel()
		return ctx.Err()
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation = %v", err)
	}
	if l.isBusy() {
		t.Fatal("idle operation left session busy")
	}
	for _, want := range []bool{true, false} {
		select {
		case ev := <-ch:
			if ev.Kind != EvStatus || ev.Busy == nil || *ev.Busy != want {
				t.Fatalf("status = %+v, want busy=%t", ev, want)
			}
		default:
			t.Fatal("missing status transition")
		}
	}
}
