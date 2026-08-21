package authz

import (
	"github.com/jack-work/figaro/api/rpc"
)

// TurnActiveFunc reports whether the named aria has a turn in flight.
type TurnActiveFunc func(ariaID string) bool

// selfForkHelp is the prose a denied self-fork returns.
const selfForkHelp = `cannot fork %[1]s from inside its own running turn: the fork would deadlock.

A fork is an inbox event, and the agent's drain loop handles one event at a
time. Issued from inside a running turn, the fork queues behind that turn -
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
