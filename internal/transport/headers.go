package transport

import (
	"context"
	"net/http"
)

// headerSinkKey is the context key under which WithHeaderSink stashes
// the caller-supplied destination. Declared as a struct{} so no other
// package can accidentally collide.
type headerSinkKey struct{}

// WithHeaderSink returns ctx carrying a destination for the next
// response's http.Header. The transport copies the 2xx response
// header set into *sink before returning the decoded payload, so
// callers can read correlation IDs, rate-limit hints, and other
// response metadata without a breaking signature change on every
// generated method.
//
// The sink is populated only on success (2xx). For errors or
// non-2xx responses the caller still gets the typed error from the
// method; sink is left untouched.
//
// Each Do/DoRaw call overwrites sink, so callers passing the same
// context into multiple sequential calls see the last one.
// Concurrent calls on the same context race; give each call its
// own sink.
func WithHeaderSink(ctx context.Context, sink *http.Header) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, headerSinkKey{}, sink)
}

// captureResponseHeaders copies hdr into the sink ctx carries, if
// any. No-op when ctx has no sink, so every code path can call it
// unconditionally.
func captureResponseHeaders(ctx context.Context, hdr http.Header) {
	if ctx == nil {
		return
	}
	sink, ok := ctx.Value(headerSinkKey{}).(*http.Header)
	if !ok || sink == nil {
		return
	}
	*sink = hdr.Clone()
}
