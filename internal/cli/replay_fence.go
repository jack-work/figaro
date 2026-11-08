package cli

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

var tapeOnly atomic.Bool

var errOffTape = errors.New("a replay has only its tape: no daemon, no vault, no config")

func refuseOffTape(what string) {
	if tapeOnly.Load() {
		die("replay reached %s: %s", what, errOffTape)
	}
}

func (in *interactiveInput) execOnTape(line string) {
	argv := tokenize(line)
	if len(argv) == 0 {
		return
	}
	verb := argv[0]
	switch verb {
	case "notifications", "no":
		in.runOverlay(verb, argv[1:])
		return
	case "form", "state":
		if name, spec, ok := liveForm(argv); ok && in.onTape(spec) {
			in.openLive(name, spec, false)
			return
		}
	}
	in.refuseOffTape(verb)
}

func (in *interactiveInput) refuseOffTape(verb string) {
	in.noteErr(fmt.Sprintf("%s: %s", verb, errOffTape))
}

func (in *interactiveInput) onTape(spec string) bool {
	host, _ := splitIntrinsic(spec)
	return host == "" || host == in.currentID()
}

func (in *interactiveInput) openTapeForm(name, spec string, full bool, keep func(*formView) bool) {
	if !in.onTape(spec) {
		in.refuseOffTape(name)
		return
	}
	_, intrinsic := splitIntrinsic(spec)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		view, closeView, _, err := openFormViewOn(ctx, cancel, in.tape, in.currentID(), intrinsic, in.renderLocked, func() {})
		if err != nil {
			in.noteErr(name + ": " + err.Error())
			return
		}
		if keep != nil && !keep(view) {
			closeView()
			return
		}
		in.installFormView(name, view, closeView, full)
	}()
}
