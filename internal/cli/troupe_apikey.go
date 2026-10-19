package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk"
)

type apiKeyOpts struct {
	TTL    string
	Static bool
}

type apiKeyDoor interface {
	TroupeAPIKeyMint(ctx context.Context, req rpc.TroupeAPIKeyMintRequest) (*rpc.TroupeAPIKeyMintResponse, error)
	TroupeAPIKeyRevoke(ctx context.Context, troupe, id string) (*rpc.TroupeAPIKeyRevokeResponse, error)
	TroupeAPIKeyList(ctx context.Context, troupe string) (*rpc.TroupeAPIKeyListResponse, error)
}

func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return apikey.DefaultTTL, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--ttl %q: want a whole number of days, like 30d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--ttl %q: want days (30d) or a duration (12h)", s)
	}
	return d, nil
}

func keyPrincipal(troupe, arg string) (string, error) {
	if !strings.Contains(arg, "@") {
		arg += "@" + apikey.Scope(troupe)
	}
	p, err := apikey.ParsePrincipal(arg, troupe)
	if err != nil {
		return "", err
	}
	return p.String(), nil
}

func apiKeyMint(ctx context.Context, d apiKeyDoor, out, info io.Writer, troupe, who string, o apiKeyOpts, jsonOut bool) error {
	principal, err := keyPrincipal(troupe, who)
	if err != nil {
		return err
	}
	ttl, err := parseTTL(o.TTL)
	if err != nil {
		return err
	}
	if ttl > apikey.MaxTTL {
		return fmt.Errorf("--ttl %s: a key lives at most %d days", o.TTL, int(apikey.MaxTTL.Hours()/24))
	}
	wire, k, err := apikey.New(rand.Reader)
	if err != nil {
		return err
	}
	if o.Static {
		return writeStaticKey(out, info, wire, k, principal, time.Now().Add(ttl).UTC().Truncate(time.Second), jsonOut)
	}
	resp, err := d.TroupeAPIKeyMint(ctx, rpc.TroupeAPIKeyMintRequest{Troupe: troupe, ID: k.ID, Principal: principal, Hash: k.Hash[:], TTLMillis: ttl.Milliseconds()})
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(struct {
			APIKey string           `json:"api_key"`
			Key    rpc.TroupeAPIKey `json:"key"`
		}{wire, resp.Key})
	}
	fmt.Fprintln(out, wire)
	r := resp.Key
	fmt.Fprintf(info, "api key %s for %s, minted by %s, signed by %s, expires %s.\nShown once: store it now (figaro troupe login %s on the machine that uses it).\n", r.ID, r.Principal, r.By, r.Signer, day(r.NotAfter), troupe)
	writeReach(info, resp.Reached, resp.Unreachable)
	return nil
}

func writeStaticKey(out, info io.Writer, wire string, k apikey.Key, principal string, notAfter time.Time, jsonOut bool) error {
	stanza := fmt.Sprintf("[[authz.api_keys]]\nid = %q\nprincipal = %q\nsha256 = %q\nnot_after = %s\n", k.ID, principal, hex.EncodeToString(k.Hash[:]), notAfter.Format(time.RFC3339))
	if jsonOut {
		return json.NewEncoder(out).Encode(struct {
			APIKey string `json:"api_key"`
			Config string `json:"config"`
		}{wire, stanza})
	}
	fmt.Fprintln(out, wire)
	fmt.Fprintf(info, "Shown once. Add this to the config of each node that takes the key; it holds the hash, never the key:\n\n%s", stanza)
	return nil
}

func apiKeyList(ctx context.Context, d apiKeyDoor, out io.Writer, troupe string, jsonOut bool) error {
	resp, err := d.TroupeAPIKeyList(ctx, troupe)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(out).Encode(resp)
	}
	scope := "keys you minted or that name you"
	if resp.All {
		scope = "every key"
	}
	fmt.Fprintf(out, "troupe %s, %s (you are %s)\n", troupe, scope, resp.Caller)
	if len(resp.Keys) == 0 {
		fmt.Fprintln(out, "  none")
		return nil
	}
	now := time.Now()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ID\tPRINCIPAL\tMINTED BY\tSIGNER\tMINTED\tEXPIRES\tSTATE")
	for _, k := range resp.Keys {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Principal, k.By, k.Signer, day(k.MintedAt), day(k.NotAfter), keyState(k, now))
	}
	return w.Flush()
}

func keyState(k rpc.TroupeAPIKey, now time.Time) string {
	switch {
	case k.RevokedAt != 0:
		return "revoked by " + k.RevokedBy + " " + day(k.RevokedAt)
	case k.Void != "":
		return "void: " + k.Void
	case !now.Before(time.UnixMilli(k.NotAfter)):
		return "expired"
	}
	return "live"
}

func apiKeyRevoke(ctx context.Context, d apiKeyDoor, out io.Writer, troupe, id string) error {
	resp, err := d.TroupeAPIKeyRevoke(ctx, troupe, id)
	if err != nil {
		return err
	}
	k := resp.Key
	if resp.Already {
		fmt.Fprintf(out, "api key %s was already revoked by %s through %s, %s\n", k.ID, k.RevokedBy, k.RevokedVia, day(k.RevokedAt))
		return nil
	}
	fmt.Fprintf(out, "api key %s (%s, minted by %s) revoked by %s through %s\n", k.ID, k.Principal, k.By, k.RevokedBy, k.RevokedVia)
	writeReach(out, resp.Reached, resp.Unreachable)
	return nil
}

func runTroupeAPIKey(args []string, jsonOut bool, o apiKeyOpts) {
	usage := "troupe apikey: want mint <troupe> <kind:name>, ls <troupe> or revoke <troupe> <id>"
	if len(args) < 2 {
		dieUsage("%s", usage)
	}
	verb, troupe := args[0], args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if verb == "mint" && len(args) == 3 && o.Static {
		if err := apiKeyMint(ctx, nil, stdout, stderrw, troupe, args[2], o, jsonOut); err != nil {
			die("troupe apikey mint: %s", err)
		}
		return
	}
	acli := localAngelus()
	defer acli.Close()
	var err error
	switch {
	case verb == "mint" && len(args) == 3:
		err = apiKeyMint(ctx, acli, stdout, stderrw, troupe, args[2], o, jsonOut)
	case verb == "ls" && len(args) == 2:
		err = apiKeyList(ctx, acli, stdout, troupe, jsonOut)
	case verb == "revoke" && len(args) == 3:
		err = apiKeyRevoke(ctx, acli, stdout, troupe, args[2])
	default:
		dieUsage("%s", usage)
	}
	if err != nil {
		die("troupe apikey %s: %s", verb, err)
	}
}

type fixedBearer string

func (b fixedBearer) Resolve() (string, error) { return string(b), nil }

func (fixedBearer) Invalidate(string) error {
	return fmt.Errorf("the door refused this api key: it may be mistyped, revoked or expired")
}

func readAPIKey(in *os.File, prompt io.Writer, name string) (string, error) {
	var raw string
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(prompt, "api key for troupe %s: ", name)
		b, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		raw = string(b)
	} else {
		line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		raw = line
	}
	key := strings.TrimSpace(raw)
	if _, err := apikey.Parse(key); err != nil {
		return "", fmt.Errorf("that is %w (fgk_...)", err)
	}
	return key, nil
}

func whoAmI(tr boot.Troupe, bearer transport.Bearer) (string, error) {
	acli, err := sdk.DialAngelus(transport.WSSEndpoint(tr.URL, bearer))
	if err != nil {
		return "", err
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := acli.TroupeAPIKeyList(ctx, "")
	if err != nil {
		return "", err
	}
	return resp.Caller, nil
}

func runAPIKeyLogin(loaded *config.Loaded, tr boot.Troupe) {
	_ = loaded
	if tr.Auth.KeyFile != "" {
		who, err := whoAmI(tr, boot.TroupeResolver(tr))
		if err != nil {
			die("troupe %s: %s", tr.Name, err)
		}
		fmt.Fprintf(stderrw, "  %s troupe %s reads its key from %s; the door knows it as %s\n", green("\u2713"), tr.Name, tr.Auth.KeyFile, who)
		return
	}
	key, err := readAPIKey(os.Stdin, stderrw, tr.Name)
	if err != nil {
		die("troupe login %s: %s", tr.Name, err)
	}
	who, err := whoAmI(tr, fixedBearer(key))
	if err != nil {
		die("troupe login %s: %s", tr.Name, err)
	}
	ensureHush()
	if err := boot.SealAPIKey(MustHush().Client(), tr, key); err != nil {
		die("troupe login %s: %s", tr.Name, err)
	}
	fmt.Fprintf(stderrw, "  %s signed in to troupe %s as %s; hush sealed the key in %s\n", green("\u2713"), tr.Name, who, dim(tr.StoredKeyPath()))
	fmt.Fprintln(stderrw, "  "+dim("next: FIGARO_TROUPE="+tr.Name+" figaro ls, or [cli] troupe = \""+tr.Name+"\""))
}

func apiKeySignIn(tr boot.Troupe) string {
	if tr.Auth.KeyFile != "" {
		return "key_file"
	}
	if _, err := os.Stat(tr.StoredKeyPath()); err == nil {
		return troupeSignedIn
	}
	return troupeSignedOut
}
