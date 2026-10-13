package cli

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
)

func (in *interactiveInput) decideComposer(question string, choices []string) {
	go func() {
		in.execDecide(rpc.DecideRequest{FigaroID: in.currentID(), Question: question, Choices: choices})
	}()
}

func (in *interactiveInput) commandDecide(args []string) {
	if len(args) == 0 {
		in.mu.Lock()
		in.lt.tr.openDecisionPit()
		in.lt.tr.render()
		in.mu.Unlock()
		return
	}
	p, err := parseDecideArgs(args)
	if err != nil {
		in.noteErr("decide: " + err.Error())
		return
	}
	if p.context == "-" {
		in.noteErr("decide: --context - reads stdin, which the pager owns; pass the text instead")
		return
	}
	if p.json {
		in.noteErr("decide: -j has no stream to shape here; the pit is the answer")
		return
	}
	aria := p.id
	if aria == "" {
		aria = in.currentID()
	}
	in.progress("…deciding")
	go in.execDecide(p.request(aria))
}

func (in *interactiveInput) execDecide(req rpc.DecideRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), decideTimeout)
	defer cancel()
	acli, err := in.angelus()
	if err != nil {
		in.noteErr("decide: " + err.Error())
		return
	}
	resp, err := acli.Decide(ctx, req)
	if err != nil {
		in.noteErr("decide: " + wireErrorText(err))
		return
	}
	in.mu.Lock()
	in.lt.tr.showDecision(req.Question, *resp)
	in.lt.tr.setCommandNote(decisionNote(*resp))
	in.lt.tr.render()
	in.mu.Unlock()
}

func decisionNote(r rpc.DecideResponse) string {
	for _, row := range r.Rows {
		if row.Chosen {
			return fmt.Sprintf("decided: %s %.2f", clipToWidth(row.Option, 24), row.Probability)
		}
	}
	return "decided: none chosen"
}
