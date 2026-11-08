<p align="center">
  <a href="https://figar.org"><img src="docs/media/banner.svg" alt="figaro, a terminal coding agent you message from your shell" width="100%"></a>
</p>

<p align="center">
  <a href="https://github.com/jack-work/figaro/releases/latest"><img src="https://img.shields.io/github/v/release/jack-work/figaro?color=98BB6C&labelColor=1F1F28&label=release" alt="latest release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-NON--AI%20MPL--2.0-7E9CD8?labelColor=1F1F28" alt="license: NON-AI MPL-2.0"></a>
  <img src="https://img.shields.io/badge/go-single%20binary-7FB4CA?labelColor=1F1F28" alt="one Go binary">
  <img src="https://img.shields.io/badge/nix-flake-957FB8?labelColor=1F1F28" alt="nix flake">
</p>

# figaro

*Largo al factotum della calcolatore.*

figaro is an AI coding agent you talk to from your terminal. Send it a message and keep using your shell; short replies print inline, long ones open in a pager. Conversations (we call them arias) live on disk, fork at any turn, and can message each other, so five agents at once is five shell commands. It's all one Go binary: the CLI and the daemon that runs the agents. Works with Claude (API key or Max subscription), GitHub Copilot, OpenRouter, Azure, and anything that speaks the OpenAI API.

A coding agent with few opinions, firmly held. Tutto pulito, bravo bravissimo.

<p align="center"><img src="docs/media/turn.gif" alt="a prompt typed in figaro's pager: the agent runs git log with its bash tool and summarizes the result" width="820"></p>

> [!NOTE]
> figaro is under heavy development. Releases go out several times a day, most of them cut by figaros themselves (the agent builds, tests and releases itself, with a human signing off). Things move and sometimes break. Pin a version if you need it to sit still; see [Releases](#releases).

## Fast from the shell

Rather than running a binary and typing a message in a chatbox, figaro wants to be passed messages from the shell using CLI verbs. `figaro -- <prompt>` and you have your shell back. That's the whole sell, really: speed.

Your arias live in a daemon called the angelus (for you catholics out there). It works like a tmux server: close the terminal, drop the SSH connection, and the turn keeps running. Attend it again from any shell and pick up where it is. The pager is a convenience on top, and the whole thing stays usable over SSH.

Jumping between agents is easy and fast, and figaro doesn't hide what its subagents do:

- `figaro attend <id>` points your shell at any of them; `figaro listen <id>` follows one live; `figaro show <id>` prints its turns, every tool call included.
- `figaro promote <id>` raises a fork in the tree at once. It's presentation only, so it never waits on history.
- `figaro send -f --id <id> -- <p>` hands one work and gets your shell back.

You can build a whole workflow out of that. This repo is mostly built that way.

## Transcripts you can fork

An aria is an actor: one inbox, one turn loop, and a transcript that is strictly append-only.

- `figaro fork <id>:12 -- <p>` branches a transcript at any turn.
- Compaction is a macro of those primitives, and the history it leaves behind stays whole. `figaro succeed @role -- <handoff>` forks the role's aria, sinks the old one beneath the new one, sends the handoff and recasts the role onto the successor. You can read exactly what was carried over, because it's a turn like any other.
- Forks share their prefix on disk and in the daemon's cache, and they reuse the provider's prompt cache. A subagent only costs the turns it adds.

## State lives in forms

Every aria has a form: key/value state the model sees as system reminders. `figaro set mantra "..."` and the model sees it next turn, no round trip. Unbound forms stand on their own, and an aria can study one: `figaro study <aria> @form`, and each change reaches it as a delta on its next turn. The model learns its environment without rewriting its prompt, so the prompt cache holds and the history stays whole.

Web API data goes into a form in one line (after `figaro form new -S name=repo` makes the form):

```bash
figaro set --id @repo stats "$(curl -s https://api.github.com/repos/jack-work/figaro | jq -c '{stars: .stargazers_count, issues: .open_issues_count}')"
```

From there it's easy to build automation that watches state and reacts: a figaro that studies the form gets each change on its next turn, `figaro form listen` follows it live, and the Go SDK (`github.com/jack-work/figaro/sdk`) dials an aria or a form, reads and patches its state, and gets every change as it lands. More languages are coming.

### Methods

Methods are small, fast (system 1) models that fill in form data on hooks, before your main model is called. The first one picks the reasoning effort for every message: [`system.methods`](skills/figaro/reference/methods.md) renders a template, a decision model picks one of its options, and the turn runs on the pick. More are coming.

## Federation and deployment

- Listen to a figaro on any server you can reach: `FIGARO_TROUPE=<name> figaro listen <id>`, signed in with `figaro troupe login <name>`.
- Several machines can federate as a troupe (`figaro troupe`): signed membership, node certificates, and arias that move between nodes.
- Every call is authorized against a principal. Grants and troupe policy decide what each one may do, and your own identity system plugs in: trust any OIDC issuer (`[[authz.issuers]]`), take users and groups from the reverse proxy in front of the web door (`authn = "upstream"`), or mint api keys (`figaro troupe apikey mint`).
- Nix for reproducible deployments: the flake, a NixOS module, a home-manager module, and a NixOS container that runs figaro as a service.
- Sandboxing and containerization are coming in gradually. `figaro box serve` already gives an aria a sandbox with only what you lend it.

Troupes are in development: solid for keys, membership and cross-node listing, not settled for moves and policy. Opening a remote aria in your own pager is being built now.

## Security, plainly

figaro isn't secure out of the box. It runs with your own user's permissions, and any aria can message any other on its node. For now it's bring your own sandbox; releases with secure modules and defaults are coming.

No MCP, by design. figaro is CLI first: its tools are bash, read, write, edit, process and image, and the model already knows how to drive a CLI. If you want an MCP server, wrap it in a CLI and sandbox it.

## Install

One static binary, no runtime deps. Pick a line.

```bash
curl -fsSL https://figar.org/install.sh | sh
```

```powershell
irm https://figar.org/install.ps1 | iex
```

```bash
nix profile install github:jack-work/figaro
go install github.com/jack-work/figaro/cmd/figaro@latest
```

The shell installer takes `--version` and `--dir`, which under a pipe go after `--`: `curl -fsSL https://figar.org/install.sh | sh -s -- --dir /usr/local/bin`. It installs to `~/.local/bin` by default, checks the release sha256 and writes shell completions.

figaro is also reachable as `fig`. Every path above installs the alias except `go install`, which needs one line: `ln -s figaro $(go env GOPATH)/bin/fig`. Config lives in `~/.config/figaro/`.

## First run

```bash
figaro provider login anthropic   # or copilot, openrouter, azure
figaro                            # opens the pager on a fresh aria
```

<p align="center"><img src="docs/media/landing.gif" alt="bare figaro opens a landing page with the blackletter logo; :new makes a fresh aria and l opens the topology" width="820"></p>

You can also sign in from the pager with `:login`. Type `>` to write a prompt and `ctrl+s` to send it. Or skip the pager entirely:

```bash
figaro -- buongiorno
```

`figaro -- :skills.howto!` starts a tutorial, taught by figaro in character.

## Everyday use

```
figaro -- <prompt>              prompt the aria this shell is attending
figaro                          open the pager (at a terminal)
figaro new -- <prompt>          a fresh aria
figaro fork -- <prompt>         branch here and prompt the branch
figaro fork <id>:12 -- <p>      branch at turn 12
figaro send -f --id <id> -- <p> message another aria and get your shell back
figaro send -r -- <prompt>      raw streamed output, for scripts
figaro ls                       your arias, as a tree
figaro attend <id>              bind this shell to an aria
figaro listen <id>              watch an aria without prompting it
figaro show <id> -n 5           the last five turns
figaro set <key> <value>        patch the form, no model round trip
figaro status                   one aria in focus
figaro models                   every model your providers offer
figaro help <command>           the rest
```

Ids are long but you rarely type them. Every aria gets a word handle (`bunny-computer`), and any unique prefix works.

Idiomatically, a shell alias or binary `q` may be defined which aliases to `fig send --`. The single character makes the standard use case, messaging figaro and staying in your shell, easier to type. I use this alias and several others to avoid CLI verbiage. Figaro is in the service of the user; figaro-specific shell configurations are idiomatic.

### The pager

In the pager:

- `>` composes, `ctrl+s` sends, `esc` backs out
- `:new`, `:fork -- <text>`, `:attend <id>` move between arias
- `l` opens the topology at the aria you're on, `L` at the root, `:ls` the whole tree
- `:state` shows the form, `:models` picks a model
- `:login` signs in to a provider, without leaving the pager
- `/` searches, `v` and `V` select, `y` yanks
- `q` leaves, and the turn keeps running

Keys are rebindable in `~/.config/figaro/keys.toml` (`figaro keys` lists them).

<p align="center"><img src="docs/media/topology.gif" alt="figaro ls prints a tree of arias with their mantras; in the pager, l opens the same tree and the cursor moves between forks" width="820"></p>

The [`figaro` skill](skills/figaro/SKILL.md) is the real manual, and it ships inside the binary so the agent always knows how to drive itself: the [CLI reference](skills/figaro/cli.md), [forms](skills/figaro/forms.md), [providers](skills/figaro/reference/providers.md), [architecture](skills/figaro/reference/architecture.md).

## Providers

| Provider | Sign in | Notes |
|---|---|---|
| Anthropic | API key, or Claude Max via OAuth | Messages API, prompt caching on by default |
| GitHub Copilot | personal or enterprise | Claude models over Messages, GPT models over the Responses WebSocket |
| OpenRouter | API key | |
| Azure OpenAI / AI Foundry | API key or Microsoft Entra | |
| any OpenAI-compatible gateway | optional key | Chat Completions or Responses |

Each lives in `~/.config/figaro/providers/<name>.toml`; [providers.md](skills/figaro/reference/providers.md) has the details, and `figaro models` shows what's reachable right now. File an issue if you want to see another provider supported.

## Releases

Most releases are cut by an aria that ran the release script after the tests and a tmux pass on the real binary. Each one's notes are on its [GitHub release](https://github.com/jack-work/figaro/releases).

- To track the latest: `nix profile install 'github:jack-work/figaro?ref=release'`, or any of the installers above. `figaro update` tells you when you're behind, and how to upgrade for the way you installed.
- To pin: `github:jack-work/figaro/v0.56.0` in a flake, `go install ...@v0.56.0`, or `install.sh -s -- --version v0.56.0`.

Pre-1.0, minors may change the on-disk format or the client/daemon protocol. When they do, the notes say so and say whether anything migrates.

## Contributing

Found something broken? File an issue. To hack on it:

```bash
nix develop                     # Go, tools, and an isolated vault and state dir
go build ./... && go test ./...
```

The dev shells keep a dev daemon away from your real store; [contributing](skills/figaro/contributing/README.md) has the rest, including how this repo's own agents work on it.

## License

[NON-AI MPL-2.0](./LICENSE). Copylot, source available for human use. Not for AI/ML training (LICENSE section 3.6).
