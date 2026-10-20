package cli

import (
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

func registerTroupe(r *cmdkit.Router) {
	r.Register(&cmdkit.Command{
		Name:  "troupe",
		Group: "System",
		Short: "Sign in to a troupe's door, list troupes, and hold this node's troupe keys",
		Usage: "troupe <login <name> | ls | join <name> | leave <name> | revoke <name> <node> | renewer <name> <node> <renewer> | keygen --out <dir> | leaf <name> --for <node-id> --public-key <hex> --door <host:port> --out <dir> | policy show <name> | policy set <name> <file|-> | apikey mint|ls|revoke <name> ... | create <name> | intermediate <name> [--for <node-id> --out <dir>]> [-j]",
		Long: `A troupe is a set of figaro nodes; troupes/<name>.toml is one as this
CLI reaches it: its url (unix:// or wss://), an optional audience, and
an [auth] table naming how a human signs in.

  login <name>   sign in to the troupe's door. scheme = "oidc-device":
                 hush holds the token as troupe:<name>. scheme =
                 "api-key": reads the key without echo (or one line of
                 stdin), checks it at the door, and hush seals it in
                 troupes/<name>.key; with key_file set, only checks it
  ls             each troupe, its url, and whether you are signed in;
                 ● marks the one FIGARO_TROUPE or [cli] troupe selects.
                 With [peers] configured, this node's troupe version and
                 what each peer last showed, from a running daemon
  create <name>        start troupe <name> on this node: the root key
                       arrives on stdin (op read <ref> | figaro troupe
                       create <name>) and is never written; root.crt and
                       this node's intermediate (one year) are
  intermediate <name>  issue or renew this node's intermediate with the
                       root on stdin: yearly, or on a node joining.
                       --for <node-id> --out <dir> issues another node's
                       instead, as root.crt, intermediate.crt and
                       intermediate.key (0600) in <dir>, which must be on
                       a tmpfs unless --allow-disk; only fingerprints are
                       printed. The root stays on this node
  join <name>    sign this node into the troupe through the member that
                 troupes/<name>.toml reaches, which needs join on
                 troupe:<name>. Shows the root's sha256 and asks before
                 trusting it; writes this node's certificate and the
                 [troupe], [door.tls] and peercred config, then restart.
                 --delegate all binds troupe:<name> to an all-granting
                 role in policy.toml, so the troupe's grants apply here;
                 --delegate none lends nothing. Asked when not given
  leave <name>   this node signs its own leave, the running daemon tells
                 every member, and its troupe keys and config go. Final
                 for this node id; asks unless --yes
  revoke <name> <node>  with the root on stdin, revoke a node and every
                 join its intermediates signed; the running daemon tells
                 every member
  keygen --out <dir>   a node key, node.key (0600) in <dir> (a tmpfs unless
                       --allow-disk), and its public key printed: for a
                       node whose key is a credential
  leaf <name> --for <node-id> --public-key <hex> --door <host:port> --out <dir>
                       with the root on stdin, issue that node a leaf from
                       this node's intermediate for the key it generated,
                       sign its join and name this node its renewer (both
                       spread by the daemon), and write root.crt, node.crt
                       and joined.json for its troupe dir. Its private key
                       never leaves it; it renews from this node
  renewer <name> <node> <renewer>  with the root on stdin, let
                 <renewer>'s intermediate issue <node>'s certificates.
                 A member takes a node's certificate only from the node's
                 own intermediate or a renewer named so; a node that joined
                 through another member needs this once
  policy show <name>   the troupe policy in force, who signed it, what
                 each member last showed, and the policy itself
  policy set <name> <file|->  sign a new version of the troupe policy
                 (needs admin on troupe:<name> in this node's own
                 policy) and tell every member. The root signs version 1
                 (--root, the key on stdin); after that a node signs only
                 while the version in force lists it in setters = [...],
                 and the root may sign any version. A troupe grant counts on
                 a node only where that node's policy lends the same
                 right to troupe:<name>; use on sandbox:local and admin
                 on the troupe are never the troupe's to grant
  apikey mint <name> <kind:name>  mint a key for user:<n> or service:<n>,
                 scoped <kind>:<n>@troupe:<name> (needs admin on the
                 troupe). The key goes to stdout once; the node keeps only
                 its hash. --nodes <id,...|all> names the nodes it is good
                 on (default: the minting node alone); it is never sent to
                 another. all is refused while any member is leaf-only. --ttl 30d (default 90d, at most 365d); for
                 everyday use mint short keys, days or weeks, and mint
                 again. --static prints the key and an [[authz.api_keys]]
                 stanza instead, with no daemon involved
  apikey ls <name>     keys with who minted each; all of them for a
                 troupe admin, else the ones you minted or that name you
  apikey revoke <name> <id>  revoke a key (its minter, its own principal
                 or a troupe admin) and tell every member`,
		Flags: []cmdkit.FlagDef{
			{Long: "json", Short: "j", IsBool: true, Description: "JSON for ls and policy show"},
			{Long: "delegate", Description: "join: all or none, what this node lends the troupe policy (required unless asked)"},
			{Long: "door", Description: "join: host:port other nodes dial (default [door.tls] advertise)"},
			{Long: "listen", Description: "join: host:port the node door listens on (default the door)"},
			{Long: "root-sha256", Description: "join: trust the troupe root only if its sha256 is this"},
			{Long: "yes", Short: "y", IsBool: true, Description: "join: trust the root shown without asking; leave: do not ask"},
			{Long: "for", Description: "intermediate: the node id to issue for, instead of this node"},
			{Long: "out", Description: "intermediate --for: the directory (a tmpfs) that receives the keys"},
			{Long: "allow-disk", IsBool: true, Description: "intermediate --for: allow an --out that is not a tmpfs"},
			{Long: "root", IsBool: true, Description: "policy set: sign with the root key on stdin (the first version needs it)"},
			{Long: "public-key", Description: "leaf: the node's Ed25519 public key in hex, as troupe keygen prints it"},
			{Long: "ttl", Description: "apikey mint: how long the key lives, like 7d or 12h (default 90d, at most 365d; keep everyday keys short)"},
			{Long: "nodes", Description: "apikey mint: the node ids the key is good on, comma-separated, or all (default: the minting node)"},
			{Long: "static", IsBool: true, Description: "apikey mint: print the key and a config stanza; no daemon"},
		},
		ArgsMin: 1,
		ArgsMax: 4,
		Run: func(ctx *cmdkit.RunContext) error {
			return runTroupeVerb(ctx.Extra.(*config.Loaded), ctx.Args, ctx.BoolFlag("json"), joinOpts{
				Delegate: ctx.Flag("delegate"), Door: ctx.Flag("door"), Listen: ctx.Flag("listen"), RootSHA256: ctx.Flag("root-sha256"), Yes: ctx.BoolFlag("yes"),
			}, keyOpts{For: ctx.Flag("for"), Out: ctx.Flag("out"), AllowDisk: ctx.BoolFlag("allow-disk"), Root: ctx.BoolFlag("root"), PublicKey: ctx.Flag("public-key")}, apiKeyOpts{TTL: ctx.Flag("ttl"), Nodes: ctx.Flag("nodes"), Static: ctx.BoolFlag("static")})
		},
		CompleteArgs: completeWords([][2]string{
			{"login", "sign in to a troupe's door"},
			{"ls", "the troupes this CLI knows"},
			{"join", "sign this node into a troupe"},
			{"leave", "sign this node out of its troupe for good"},
			{"revoke", "with the root, revoke a node"},
			{"renewer", "with the root, name a node's renewer"},
			{"keygen", "a node key for a credential"},
			{"leaf", "with the root, issue another node's leaf for its key"},
			{"policy", "show or set the troupe policy"},
			{"apikey", "mint, list or revoke api keys"},
		}, completeTroupeAfterLogin),
	})
}
