package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/quote"
)

func isCommentVerb(line string) bool {
	verb, _, _ := strings.Cut(strings.TrimSpace(line), " ")
	return verb == "comment"
}

func (in *interactiveInput) commandComment(line string) {
	cmd, err := parseCommentCommand(line)
	if err != nil {
		in.noteErr(err.Error())
		return
	}
	if cmd.del {
		in.confirmDelete(cmd)
		return
	}
	in.commandAsync(func(ctx context.Context) (string, error) {
		return in.comment(ctx, cmd)
	})
}

func (in *interactiveInput) confirmDelete(cmd commentCommand) {
	c, _, err := in.resolveComment(cmd.addr, cmd.haveAddr)
	if err != nil {
		in.noteErr(err.Error())
		return
	}
	prompt := "delete comment " + commentAddress(c, 0) + "?"
	if n := len(c.thread); n > 1 {
		prompt = "delete comment " + commentAddress(c, 0) + " and its " + strconv.Itoa(n) + " messages?"
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.lt.tr.askConfirm(prompt, func() {
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.deleteComment(ctx, c)
		})
	})
}

type commentAddr struct {
	id    string
	reply int
}

func parseCommentAddr(s string) (commentAddr, bool) {
	s = strings.TrimPrefix(s, "#")
	if s == "" || strings.ContainsAny(s, " \t<>!") {
		return commentAddr{}, false
	}
	id, k, hasK := strings.Cut(s, ".")
	a := commentAddr{id: id}
	if hasK {
		n, err := strconv.Atoi(k)
		if err != nil || n < 1 {
			return commentAddr{}, false
		}
		a.reply = n
	}
	return a, true
}

type commentCommand struct {
	del      bool
	addr     commentAddr
	haveAddr bool
	passage  *quote.Range
	text     string
	hasText  bool
}

func parseCommentCommand(line string) (commentCommand, error) {
	var cmd commentCommand
	line = strings.TrimSpace(line)
	head, tail, hasText := cutVerbBoundary(line)
	if !hasText {
		head = line
	}
	cmd.hasText = hasText
	fields := strings.Fields(head)
	if len(fields) > 0 {
		fields = fields[1:]
	}
	for _, f := range fields {
		switch {
		case f == "--":
		case f == "-X" || f == "--delete":
			cmd.del = true
		case !cmd.haveAddr:
			a, ok := parseCommentAddr(f)
			if !ok {
				return cmd, fmt.Errorf("comment: %q is neither a flag nor a comment id (#id, #id.2)", f)
			}
			cmd.addr, cmd.haveAddr = a, true
		default:
			return cmd, fmt.Errorf("comment: one comment at a time (%q)", f)
		}
	}
	text := strings.TrimSpace(tail)
	r, rest, err := quote.Parse(text)
	switch {
	case err == nil:
		if cmd.del || cmd.haveAddr {
			return cmd, errors.New("comment: a passage starts a new comment; -X and an id name an existing one")
		}
		cmd.passage, cmd.text = &r, strings.TrimSpace(rest)
		return cmd, nil
	case !errors.Is(err, quote.ErrNone):
		return cmd, fmt.Errorf("comment: %w", err)
	}
	if first, rest, _ := strings.Cut(text, " "); strings.HasPrefix(first, "#") {
		if a, ok := parseCommentAddr(first); ok {
			if cmd.haveAddr && a != cmd.addr {
				return cmd, errors.New("comment: two different comments named")
			}
			cmd.addr, cmd.haveAddr, text = a, true, strings.TrimSpace(rest)
		}
	}
	cmd.text = text
	if cmd.del && cmd.text != "" {
		return cmd, errors.New("comment: -X deletes; it takes no text")
	}
	return cmd, nil
}

func (in *interactiveInput) comment(ctx context.Context, cmd commentCommand) (string, error) {
	if cmd.passage != nil {
		return in.newComment(ctx, *cmd.passage, cmd.text)
	}
	c, _, err := in.resolveComment(cmd.addr, cmd.haveAddr)
	if err != nil {
		return "", err
	}
	if cmd.text == "" {
		return "", errors.New("comment: nothing to say (:comment " + commentAddress(c, 0) + " -- <text>)")
	}
	return in.reply(ctx, c, cmd.text)
}

func (in *interactiveInput) newComment(ctx context.Context, r quote.Range, text string) (string, error) {
	if text == "" {
		return "", errors.New("comment: nothing to say (:<,>comment -- <text>)")
	}
	in.mu.Lock()
	turn := in.lt.tr.turnOfLT(r.Start.LT)
	in.mu.Unlock()
	taken := map[string]bool{}
	for _, c := range in.boundFormComments().list {
		taken[c.id] = true
	}
	id := newCommentID(taken)
	rec := commentRecord{
		At:     quote.Format(r),
		Turn:   turn,
		Thread: map[string]commentEntryRecord{appendKey(nil): {By: commenter(), Text: text}},
	}
	if err := in.writeBoundForm(ctx, commentsKey+"."+id, rec); err != nil {
		return "", fmt.Errorf("comment: %w", err)
	}
	return "commented #" + id + " on " + rec.At, nil
}

func (in *interactiveInput) boundFormComments() *commentIndex {
	var snap form.Snapshot
	if bound := in.subjectBoundForm(); bound != nil {
		snap, _, _ = bound.state()
	}
	return readComments(snap)
}

func (in *interactiveInput) resolveComment(addr commentAddr, named bool) (comment, int, error) {
	if named {
		for _, c := range in.boundFormComments().list {
			if c.id != addr.id {
				continue
			}
			if addr.reply > len(c.thread) {
				return comment{}, 0, fmt.Errorf("comment: #%s has %d messages, not %d", c.id, len(c.thread), addr.reply)
			}
			return c, addr.reply, nil
		}
		return comment{}, 0, fmt.Errorf("comment: no comment #%s on this aria", addr.id)
	}
	in.mu.Lock()
	tr := in.lt.tr
	var c comment
	found, reply := false, 0
	if tr.selection.active {
		c, found = tr.commentOf(tr.selection.focus.nodeRef)
		reply = tr.selection.focus.reply
	}
	in.mu.Unlock()
	if !found {
		return comment{}, 0, errors.New("comment: highlight a passage (v) to comment on it, select a comment (c j), or name one (#id)")
	}
	return c, reply, nil
}

func (in *interactiveInput) reply(ctx context.Context, c comment, text string) (string, error) {
	key := appendKey(c.thread)
	if err := in.writeBoundForm(ctx, commentsKey+"."+c.id+".thread."+key, commentEntryRecord{By: commenter(), Text: text}); err != nil {
		return "", fmt.Errorf("comment: %w", err)
	}
	return "replied to " + commentAddress(c, 0), nil
}

func (in *interactiveInput) deleteComment(ctx context.Context, c comment) (string, error) {
	if err := in.removeBoundForm(ctx, commentsKey+"."+c.id); err != nil {
		return "", fmt.Errorf("comment: %w", err)
	}
	return "deleted " + commentAddress(c, 0), nil
}

func newCommentID(taken map[string]bool) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	for size := 3; ; size++ {
		for range 64 {
			b := make([]byte, size)
			for i := range b {
				b[i] = alphabet[rand.IntN(len(alphabet))]
			}
			if id := string(b); !taken[id] {
				return id
			}
		}
	}
}

func appendKey(thread []commentEntry) string {
	key := strconv.FormatInt(time.Now().UnixMilli(), 36)
	last := ""
	for _, e := range thread {
		if e.key > last {
			last = e.key
		}
	}
	if key <= last {
		key = last + "0"
	}
	return key
}

func commenter() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "you"
}
