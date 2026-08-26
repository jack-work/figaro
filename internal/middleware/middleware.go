// Package middleware is decorator composition, and nothing else.
//
// It has one type and one function, no domain knowledge, and no dependencies
// beyond the language. That is deliberate: the moment this package knows what
// an RPC or an HTTP request is, it stops being the thing both of them can
// share, and figaro grows a second Chain somewhere else.
package middleware

// Middleware decorates a handler of type H, returning a handler of the same
// type. The shape is go-kit's `func(Endpoint) Endpoint` rather than gRPC's
// interceptor: the next handler arrives by CLOSURE, not as an argument.
//
// Naming, since it is a fair question: the Go ecosystem splits the word by
// transport -- net/http says middleware, gRPC and connectrpc say interceptor.
// A gRPC interceptor is not a decorator (its signature is
// `(ctx, req, info, handler)`), so borrowing that word for a `func(H) H`
// would import the name without the convention. go-kit generalised
// middleware off HTTP to exactly this shape, so middleware it is.
type Middleware[H any] func(next H) H

// Chain composes middlewares into one, LEFT TO RIGHT AND OUTERMOST FIRST:
//
//	Chain(a, b, c)(final)  ==  a(b(c(final)))
//
// so a sees the request first and final sees it last. That is the http
// convention and the plain reading of the argument list, and the reversed
// loop below is what makes the two agree.
//
// THE ORDER IS LOAD-BEARING, not cosmetic. Authorization must run before
// dressing, because dressing resolves outfit names by READING FILES FROM
// DISK, and an unauthorized caller must never reach a path that touches the
// filesystem. A Chain that quietly reversed would turn that invariant into a
// coin flip while every test still passed, which is why the order has a test
// of its own rather than a comment of its own.
func Chain[H any](ms ...Middleware[H]) Middleware[H] {
	return func(final H) H {
		for i := len(ms) - 1; i >= 0; i-- {
			final = ms[i](final)
		}
		return final
	}
}

// Chain of nothing is the identity, and that is the ONLY way to say "no
// decoration". There is deliberately no nilable Middleware anywhere in
// figaro: a nil hook meaning "unguarded" is how the aria endpoint served its
// entire agency surface with no policy for a year. Openness has to be a
// sentence someone typed.
