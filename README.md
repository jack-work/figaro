<p align="center">
  <a href="https://figar.org"><img src="docs/media/banner.svg" alt="figaro, a programmer's harness" width="100%"></a>
</p>

<p align="center">
  <a href="https://github.com/jack-work/figaro/releases/latest"><img src="https://img.shields.io/github/v/release/jack-work/figaro?color=98BB6C&labelColor=1F1F28&label=release" alt="latest release"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-NON--AI%20MPL--2.0-7E9CD8?labelColor=1F1F28" alt="license: NON-AI MPL-2.0"></a>
  <img src="https://img.shields.io/badge/go-single%20binary-7FB4CA?labelColor=1F1F28" alt="one Go binary">
  <img src="https://img.shields.io/badge/nix-flake-957FB8?labelColor=1F1F28" alt="nix flake">
</p>

*Largo al factotum della calcolatore.*

figaro is a programmer's harness: AI coding agents you run and fork from your terminal. Send it a message and keep using your shell; short replies print inline, long ones open in a pager. Conversations (we call them arias) live on disk, fork at any turn, and can message each other. It's all one Go binary: the CLI and the daemon that runs the agents, and the daemon keeps well over a thousand of them in under 500 MB of memory. It runs fast on a laptop from 2004. Works with Claude (API key or Max subscription), GitHub Copilot, OpenRouter, Azure, and any OpenAI-compatible endpoint.

A coding agent with few opinions, firmly held. Tutto pulito, bravo bravissimo.

<p align="center"><img src="docs/media/engine.gif" alt="from a plain shell: two arias prompted in the background, figaro ls with both running, a fork prompted at the head, a form made, studied and set, the aria answering about the change, and figaro show printing a turn" width="820"></p>

> [!NOTE]
> figaro is under heavy development. Releases go out several times a day, most of them cut by figaros themselves (the agent builds, tests and releases itself, with a human signing off). Things move and sometimes break. Pin a version if you need it to sit still; see [Releases](#releases).

## Fast from the shell

Rather than running a binary and typing a message in a chatbox, figaro wants to be passed messages from the shell using CLI verbs. `figaro -- <prompt>` and you have your shell back. It's fast, and that's a big part of the point.

Your arias live in a daemon called the angelus (for you catholics out there). It works like a tmux server: close the terminal, drop the SSH connection, and the turn keeps running. Attend it again from any shell and pick up where it is. The pager is a convenience on top, and the whole thing stays usable over SSH.

Jumping between agents is easy and fast, and figaro doesn't hide what its subagents do:

- `figaro attend <id>` points your shell at any of them; `figaro listen <id>` follows one live; `figaro show <id>` prints its turns, every tool call included.
- `figaro promote <id>` raises a fork in the tree at once. It's presentation only, so it never waits on history.
- `figaro send -f --id <id> -- <p>` hands one work and gets your shell back.

You can build a whole workflow out of that. This repo is mostly built that way.

## Running a lot of them

figaro is built to keep hundreds of agents around on thin margins. My store holds about 1,700 of them, and the angelus serving them all sits around 320 MB of memory, under a 512 MB cap (`[memory] soft_limit_mb = 512`, the Go runtime's soft memory limit; the default is 2048). It doesn't stagger. I daily-drive it at my big tech employer: hundreds of hours of heavy use so far, and it cooks.

What makes that many livable:

- Switching: in the pager `^O` and `Tab` step back and forward through the arias you've visited, `a` attends the one under the cursor, `^` its parent.
- Finding: `/` in `:ls` is a fuzzy finder over handles, ids and mantras.
- Comments: select a passage and `:comment -- <text>` pins a note to it; `c j` and `c k` jump between them.
- Links into your editor: in visual mode `M-Enter` opens the path under the cursor in your editor, at the line when it has one (`client.go:42`).
- Durability: every log is append-only. A prompt sent while the angelus restarts is spooled to disk and delivered once it's back, and clients reconnect on their own.

## Transcripts you can fork

An aria is an actor: one inbox, one turn loop, and a transcript that is strictly append-only.

- `figaro fork <id>:12 -- <p>` branches a transcript at any turn.
- Compaction is a macro of those primitives, and the history it leaves behind stays whole. `figaro succeed @role -- <handoff>` forks the role's aria, sinks the old one beneath the new one, sends the handoff and recasts the role onto the successor. You can read exactly what was carried over, because it's a turn like any other.
- Forks share their prefix on disk and in the daemon's cache. A subagent only costs the turns it adds.

## The prompt cache stays warm

Providers keep a KV cache: for a prompt prefix they have already processed, they hold on to the attention keys and values, and a request that starts with the same bytes skips that work. Reading from it is cheaper and faster than sending fresh input. Anthropic bills cache reads at 0.1x the base input price (0.05x on Opus 5.5 and Sonnet 5.5) and cache writes at 1.25x ([prompt caching](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching)). OpenAI discounts cached input by up to 95% and charges 1.25x for cache writes on GPT-5.6 and later ([prompt caching](https://platform.openai.com/docs/guides/prompt-caching)). Both say a cached prefix shortens the time to the first token.

One changed byte early in a prompt invalidates the cache for everything after it. Inside an aria, figaro keeps the prefix byte-stable:

- Each provider's translation of a message is written once to its own channel and sent byte for byte on every later turn.
- State the model sees (the mantra, skills, studied forms) arrives as reminders on the newest message, never as edits to old ones.
- Forks and `figaro succeed` successors start from their parent's bytes, so a subagent begins on a warm prefix.
- A model can't read another model's cache, so switching models costs one cold turn on the new one. History isn't rewritten, so each model's prefix stays the same, and switching back finds it cached for as long as the provider keeps it. Changing the credo does change the system prompt, and the cache rebuilds from there.

Across the 1,646 arias in my store since July, 93% of input tokens were cache reads (91% over the last week). That's `cache_read_tokens` over cache reads, cache writes and uncached input, summed from `figaro ls -j`.

## State lives in forms

A form is a durable object: key/value state with a log of patches behind it. Every aria has one (the model sees it as system reminders), and unbound forms stand on their own.

- Writes are append-only. A patch is appended to the form's log on disk and fsync'd before anyone can read it; concurrent writes share one fsync.
- In memory a form is an immutable AVL tree. Each write copies only the path it touches and publishes a new version, so any number of readers see a complete snapshot without taking a lock.
- Because a form is a log, a cursor can fold the patches since the model last looked into one delta. That's what `figaro study <aria> @form` does: the change rides in as a reminder on the aria's next message. It doesn't start a turn and doesn't wait on one, and the old messages stay as they were.
- Each form has its own writer, so config and state live in separate concurrency domains. A form takes writes and broadcasts them while the model is busy, without blocking it.

So online data can become a form that your figaros study. One line turns a web API into one (after `figaro form new -S name=repo` makes the form):

```bash
figaro set --id @repo stats "$(curl -s https://api.github.com/repos/jack-work/figaro | jq -c '{stars: .stargazers_count, issues: .open_issues_count}')"
```

Pair that with a heartbeat or a `figaro send` from your own automation and your agents respond to changes in your data as they happen. `figaro form listen` follows a form live, and the Go SDK (`github.com/jack-work/figaro/sdk`) dials an aria or a form, reads and patches its state, and gets every change as it lands. More languages are coming.

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
figaro -- buongiorno
```

Bare `figaro` opens the same aria in [the pager](#the-pager) instead. `figaro -- :skills.howto!` starts a tutorial, taught by figaro in character.

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

The pager is a view over the same arias: for reading a long turn, scrolling back, or moving around without typing commands. Bare `figaro` opens it, and `q` gives you the shell back.

<p align="center"><img src="docs/media/turn.gif" alt="a prompt typed in figaro's pager: the agent runs git log with its bash tool and summarizes the result" width="820"></p>

<p align="center"><img src="docs/media/landing.gif" alt="bare figaro opens a landing page with the blackletter logo; :new makes a fresh aria and l opens the topology" width="820"></p>


- `>` composes, `ctrl+s` sends, `esc` backs out
- `:new`, `:fork -- <text>`, `:attend <id>` move between arias
- `l` opens the topology at the aria you're on, `L` at the root, `:ls` the whole tree
- `:state` shows the form, `:models` picks a model
- `:login` signs in to a provider, without leaving the pager
- `/` searches, `v` and `V` select, `y` yanks
- `q` leaves, and the turn keeps running

Keys are rebindable in `~/.config/figaro/keys.toml` (`figaro keys` lists them).


The [`figaro` skill](skills/figaro/SKILL.md) is the real manual, and it ships inside the binary so the agent always knows how to drive itself: the [CLI reference](skills/figaro/cli.md), [forms](skills/figaro/forms.md), [providers](skills/figaro/reference/providers.md), [architecture](skills/figaro/reference/architecture.md).

## Providers

| Provider | Sign in | Notes |
|---|---|---|
| Anthropic | API key, or Claude Max via OAuth | Messages API, prompt caching on by default |
| GitHub Copilot | personal or enterprise | Claude models over Messages, GPT models over the Responses WebSocket |
| OpenRouter | API key | |
| Azure OpenAI / AI Foundry | API key or Microsoft Entra | |
| anything OpenAI-compatible | optional key | point a provider file's `base_url` at it; Chat Completions or Responses |

Each lives in `~/.config/figaro/providers/<name>.toml`. A local model server or your company's gateway is one file with a `base_url`; [providers.md](skills/figaro/reference/providers.md) has the details, and `figaro models` shows what's reachable right now. File an issue if you want to see another provider supported.

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
