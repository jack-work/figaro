package toolout

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewDefaultMaxLines(t *testing.T) {
	for _, mx := range []int{-1, 0} {
		g := New(mx)
		if g.maxLines != defaultMaxLines {
			t.Fatalf("New(%d).maxLines = %d, want %d", mx, g.maxLines, defaultMaxLines)
		}
	}
	if New(3).maxLines != 3 {
		t.Fatal("explicit maxLines not honored")
	}
}

func TestClampTail(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 2, "b\nc"},
		{"a\nb\nc\n", 3, "a\nb\nc\n"},
		{"a\nb\nc\n", 10, "a\nb\nc\n"},
		{"only-partial", 3, "only-partial"},
		{"one\n", 1, "one\n"},
		{"a\nb\nc\nd\ne", 3, "c\nd\ne"},
	}
	for _, c := range cases {
		got := clampTail(c.in, c.n)
		if got != c.want {
			t.Errorf("clampTail(%q,%d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestFeedBoundedTail(t *testing.T) {
	g := New(5)
	const total = 10000
	// Feed one line at a time.
	for i := 0; i < total; i++ {
		g.Feed("k", fmt.Sprintf("line-%d\n", i))
	}
	tail := g.Tail("k")
	lines := strings.Split(strings.TrimSuffix(tail, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("tail has %d lines, want 5: %q", len(lines), tail)
	}
	for i, ln := range lines {
		want := fmt.Sprintf("line-%d", total-5+i)
		if ln != want {
			t.Errorf("line %d = %q, want %q", i, ln, want)
		}
	}
	// Bound: retained text should be small (well under 200 bytes).
	if len(tail) > 200 {
		t.Errorf("tail length %d exceeds bound", len(tail))
	}
}

func TestFeedManyChunksBounded(t *testing.T) {
	g := New(3)
	// Fed as one large chunk with 10k lines then more.
	var b strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&b, "L%d\n", i)
	}
	g.Feed("k", b.String())
	g.Feed("k", "extra1\nextra2\nextra3\nextra4\n")
	tail := g.Tail("k")
	if tail != "extra2\nextra3\nextra4\n" {
		t.Errorf("got %q", tail)
	}
}

func TestSplitLineAcrossFeeds(t *testing.T) {
	g := New(10)
	g.Feed("k", "hello ")
	g.Feed("k", "world")
	if got := g.Tail("k"); got != "hello world" {
		t.Errorf("partial reassembly: %q", got)
	}
	g.Feed("k", "\nnext")
	if got := g.Tail("k"); got != "hello world\nnext" {
		t.Errorf("after newline: %q", got)
	}
}

func TestSplitLineWithClamp(t *testing.T) {
	g := New(2)
	g.Feed("k", "a\nb\nhel")
	g.Feed("k", "lo")
	// Lines: a, b, hello -> keep last 2: b, hello
	if got := g.Tail("k"); got != "b\nhello" {
		t.Errorf("got %q", got)
	}
}

func TestDirtyLifecycle(t *testing.T) {
	g := New(5)
	if g.Dirty() {
		t.Fatal("fresh governor should be clean")
	}
	g.Feed("k", "x")
	if !g.Dirty() {
		t.Fatal("Feed should mark dirty")
	}
	g.ClearDirty()
	if g.Dirty() {
		t.Fatal("ClearDirty failed")
	}
	// Empty feed does not dirty.
	g.Feed("k", "")
	if g.Dirty() {
		t.Fatal("empty Feed should not dirty")
	}
	g.Feed("k2", "y")
	if !g.Dirty() {
		t.Fatal("any key should dirty global")
	}
}

func TestDropAndUnknown(t *testing.T) {
	g := New(5)
	if g.Tail("nope") != "" {
		t.Fatal("unknown key should be empty")
	}
	g.Feed("k", "hi\n")
	if g.Tail("k") != "hi\n" {
		t.Fatal("Feed not stored")
	}
	g.Drop("k")
	if g.Tail("k") != "" {
		t.Fatal("Drop did not forget")
	}
	// Drop of unknown is a no-op.
	g.Drop("never")
}
