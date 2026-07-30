package authz

import (
	"github.com/jack-work/figaro/internal/rpc"
)

// TurnActiveFunc reports whether the named aria has a turn in flight.
//
// The policy cannot know this on its own — it is agent state, owned by the
// registry — so it is injected. Keeping it a func rather than an interface also
// keeps authz free of any dependency on internal/figaro or internal/angelus,
// which is what lets those packages import authz instead of the reverse.
type TurnActiveFunc func(ariaID string) bool

// selfForkHelp is the prose a denied self-fork returns.
//
// An error that only says "denied" is a bug report waiting to happen. This one
// has to teach the workaround, because the workaround is not guessable: the
// caller must DETACH the fork so it runs after the turn that is blocking it.
//
// The %[1]s is the caller's own aria id, so the recipe is copy-pasteable rather
// than a template the reader has to fill in while confused.
const selfForkHelp = `cannot fork %[1]s from inside its own running turn: the fork would deadlock.

A fork is an inbox event, and the agent's drain loop handles one event at a
time. Issued from inside a running turn, the fork queues behind that turn —
and the turn cannot finish while your tool call waits on the fork. Neither
side can move.

Detach it so it lands after this turn closes:

  mkdir -p /var/tmp/%[1]s
  cat > /var/tmp/%[1]s/fork.sh <<'SH'
  #!/usr/bin/env bash
  set -u
  exec >>/var/tmp/%[1]s/fork.log 2>&1
  figaro fork --id %[1]s --stay -j > /var/tmp/%[1]s/fork.json
  jq -r .alternative /var/tmp/%[1]s/fork.json
  SH
  chmod +x /var/tmp/%[1]s/fork.sh
  env -u FIGARO_ARIA -u FIGARO_NO_BIND setsid nohup /var/tmp/%[1]s/fork.sh >/dev/null 2>&1 &

Read /var/tmp/%[1]s/fork.json on your next turn to learn the new ids. Unset
FIGARO_ARIA and FIGARO_NO_BIND as shown, or the detached child is attributed
back to you and lands in the same trap.

To brief the fork, add a send after the fork in that script:
  figaro send --id "$(jq -r .alternative /var/tmp/%[1]s/fork.json)" -f -- "<prompt>"`

// NoSelfForkDuringTurn denies an aria forking ITSELF while its own turn runs.
//
// This is a GUARDRAIL, NOT A FIX. The defect is that fork coordination rides
// the agent's single-threaded event loop at all; see the note at
// angelus.handlers.fork. Policing the call is what we can do cheaply today
// without restructuring trunk storage.
//
// Three conditions, all necessary:
//
//   - the method is figaro.fork — no other method coordinates through the
//     inbox this way;
//   - the caller is authenticated AND is the fork target. An anonymous caller
//     is a human or an external script, and is not inside the turn, so it must
//     not be denied. A DIFFERENT aria forking this one is also fine: its tool
//     call blocks, but the target's drain loop is free to service the fork;
//   - the target actually has a turn in flight. Forking yourself while idle is
//     legitimate and common — that is what a detached script does.
func NoSelfForkDuringTurn(turnActive TurnActiveFunc) Rule {
	return Rule{
		Name: "no-self-fork-during-turn",
		Check: func(r Request) Decision {
			if r.Method != rpc.MethodFork {
				return Allow()
			}
			if !r.Identity.Authenticated || !r.SelfTargeted() {
				return Allow()
			}
			if turnActive == nil || !turnActive(r.Identity.FigaroID) {
				return Allow()
			}
			return Deny(selfForkHelp, r.Identity.FigaroID)
		},
	}
}

// DefaultRules is the policy config selects when authorization is switched on.
// It is a table so the composition is readable at a glance and so adding a rule
// is a one-line data change rather than a code change at a call site.
func DefaultRules(turnActive TurnActiveFunc) Rules {
	return Rules{
		NoSelfForkDuringTurn(turnActive),
	}
}
