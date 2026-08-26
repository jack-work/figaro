package middleware

import (
	"net/http"
	"strings"
	"testing"
)

type handler func(*strings.Builder)

func tag(name string) Middleware[handler] {
	return func(next handler) handler {
		return func(b *strings.Builder) {
			b.WriteString(">" + name)
			next(b)
			b.WriteString("<" + name)
		}
	}
}

// THE ORDER TEST. Chain(a, b, c) means a is OUTERMOST and sees the request
// first. Reversing this silently inverts security ordering -- authz would run
// after dressing, so an unauthorized caller would reach the filesystem before
// anything refused them -- and every other test in the tree would still pass.
func TestChainAppliesLeftToRightOutermostFirst(t *testing.T) {
	var b strings.Builder
	Chain(tag("a"), tag("b"), tag("c"))(func(b *strings.Builder) {
		b.WriteString("|final|")
	})(&b)

	const want = ">a>b>c|final|<c<b<a"
	if got := b.String(); got != want {
		t.Fatalf("chain order wrong\n got: %s\nwant: %s\n\n"+
			"Chain(a,b,c) must mean a(b(c(final))): a sees the request first.", got, want)
	}
}

// Chain of nothing is the identity. This is the only supported way to express
// "no decoration": there is no nil Middleware in figaro, because a nil hook
// meaning "open" is the bug this whole refactor is downstream of.
func TestEmptyChainIsIdentity(t *testing.T) {
	var b strings.Builder
	Chain[handler]()(func(b *strings.Builder) { b.WriteString("bare") })(&b)
	if b.String() != "bare" {
		t.Fatalf("empty chain is not the identity: %q", b.String())
	}
}

func TestSingleMiddleware(t *testing.T) {
	var b strings.Builder
	Chain(tag("only"))(func(b *strings.Builder) { b.WriteString("|x|") })(&b)
	if got, want := b.String(), ">only|x|<only"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// A middleware that does not call next must be able to stop the chain dead.
// This is how a policy refusal works: the request never reaches the handler,
// so a guard that ran but could not PREVENT would be decoration, not a guard.
func TestMiddlewareCanShortCircuit(t *testing.T) {
	reached := false
	stop := func(handler) handler {
		return func(b *strings.Builder) { b.WriteString("refused") }
	}
	var b strings.Builder
	Chain[handler](stop, tag("never"))(func(*strings.Builder) { reached = true })(&b)

	if reached {
		t.Fatal("a short-circuiting middleware still reached the final handler")
	}
	if b.String() != "refused" {
		t.Fatalf("got %q", b.String())
	}
}

// The point of the type parameter: one Chain for every decorator shape in the
// tree. If this stops compiling, Chain has grown a domain opinion it should
// not have.
func TestChainIsGenericOverHandlerShape(t *testing.T) {
	var order []string
	note := func(name string) Middleware[http.Handler] {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(note("outer"), note("inner"))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			order = append(order, "final")
		}))
	h.ServeHTTP(nil, nil)

	if strings.Join(order, ",") != "outer,inner,final" {
		t.Fatalf("http.Handler chain ran %v", order)
	}
}
