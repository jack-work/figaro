package troupe

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

type Kind string

const (
	Join    Kind = "join"
	Leave   Kind = "leave"
	Revoke  Kind = "revoke"
	Policy  Kind = "policy"
	Renewer Kind = "renewer"
)

const MaxEntries = 4096

const entryDomain = "figaro troupe entry v1\n"

type Entry struct {
	Kind      Kind     `json:"kind"`
	Troupe    string   `json:"troupe"`
	Node      string   `json:"node"`
	Door      string   `json:"door,omitempty"`
	Owner     string   `json:"owner,omitempty"`
	Version   uint64   `json:"version,omitempty"`
	By        string   `json:"by,omitempty"`
	Body      string   `json:"body,omitempty"`
	Renews    string   `json:"renews,omitempty"`
	Key       string   `json:"key,omitempty"`
	Principal string   `json:"principal,omitempty"`
	Hash      []byte   `json:"hash,omitempty"`
	NotAfter  int64    `json:"not_after,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	At        int64    `json:"at"`
	Chain     [][]byte `json:"chain,omitempty"`
	Sig       []byte   `json:"sig"`
}

func (e Entry) message() []byte {
	switch e.Kind {
	case Policy:
		return e.policyMessage()
	case Renewer:
		return e.renewerMessage()
	case APIKey, APIKeyRevoke:
		return e.apiKeyMessage()
	}
	body, _ := json.Marshal(struct {
		Kind   Kind   `json:"kind"`
		Troupe string `json:"troupe"`
		Node   string `json:"node"`
		Door   string `json:"door"`
		Owner  string `json:"owner"`
		At     int64  `json:"at"`
	}{e.Kind, e.Troupe, e.Node, e.Door, e.Owner, e.At})
	return append([]byte(entryDomain), body...)
}

func (e Entry) ID() string {
	h := sha256.New()
	h.Write(e.message())
	h.Write(e.Sig)
	for _, c := range e.Chain {
		h.Write(c)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (e Entry) Time() time.Time { return time.UnixMilli(e.At) }

func sign(e Entry, key crypto.Signer) (Entry, error) {
	sig, err := key.Sign(rand.Reader, e.message(), crypto.Hash(0))
	if err != nil {
		return Entry{}, err
	}
	e.Sig = sig
	return e, nil
}

func SignJoin(key crypto.Signer, inter *x509.Certificate, troupe, node, door, owner string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Join, Troupe: troupe, Node: node, Door: door, Owner: owner, At: at.UnixMilli(), Chain: [][]byte{inter.Raw}}, key)
}

func SignLeave(key crypto.Signer, leaf, inter *x509.Certificate, troupe, node string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Leave, Troupe: troupe, Node: node, At: at.UnixMilli(), Chain: [][]byte{leaf.Raw, inter.Raw}}, key)
}

func SignRevoke(root ed25519.PrivateKey, troupe, node string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Revoke, Troupe: troupe, Node: node, At: at.UnixMilli()}, root)
}

type verified struct {
	Entry
	chain []*x509.Certificate
}

func (v verified) signer() string {
	if v.Kind == Join || v.Kind == Policy {
		return IntermediateNode(v.chain[0])
	}
	return ""
}

func verify(e Entry, troupe string, root *x509.Certificate) (verified, error) {
	out := verified{Entry: e}
	if e.Troupe != troupe {
		return out, fmt.Errorf("entry for troupe %q, not %q", e.Troupe, troupe)
	}
	if !e.RootSigned() {
		if err := rpc.ValidateAriaID(e.Node); err != nil {
			return out, fmt.Errorf("node: %w", err)
		}
	}
	if e.At <= 0 {
		return out, errors.New("no time")
	}
	for _, der := range e.Chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return out, err
		}
		out.chain = append(out.chain, c)
	}
	at := e.Time()
	var pub crypto.PublicKey
	if e.Kind != Policy && !isKeyKind(e.Kind) && (e.Version != 0 || e.By != "" || e.Body != "") {
		return out, fmt.Errorf("a %s carries no policy", e.Kind)
	}
	if e.Kind != Renewer && e.Renews != "" {
		return out, fmt.Errorf("a %s names no renewer", e.Kind)
	}
	if !isKeyKind(e.Kind) && (e.Key != "" || e.Principal != "" || e.Hash != nil || e.NotAfter != 0 || e.Nodes != nil) {
		return out, fmt.Errorf("a %s carries no api key", e.Kind)
	}
	switch e.Kind {
	case APIKey, APIKeyRevoke:
		k, err := verifyAPIKey(out, root, at)
		if err != nil {
			return out, err
		}
		pub = k
	case Policy:
		key, err := verifyPolicy(out, root, at)
		if err != nil {
			return out, err
		}
		pub = key
	case Join:
		if _, _, err := net.SplitHostPort(e.Door); err != nil {
			return out, fmt.Errorf("door %q: %w", e.Door, err)
		}
		if err := ValidOwner(e.Owner); err != nil {
			return out, err
		}
		if len(out.chain) != 1 {
			return out, errors.New("a join carries the admitting intermediate alone")
		}
		inter := out.chain[0]
		if !inter.IsCA || inter.MaxPathLen != 0 || IntermediateNode(inter) == "" {
			return out, errors.New("a join is signed by a node intermediate")
		}
		if err := chainsTo(inter, nil, root, at); err != nil {
			return out, err
		}
		pub = inter.PublicKey
	case Leave:
		if e.Door != "" || e.Owner != "" || len(out.chain) != 2 {
			return out, errors.New("a leave carries the node's certificate and its issuer, and no door")
		}
		leaf := out.chain[0]
		if err := chainsTo(leaf, out.chain[1], root, at); err != nil {
			return out, err
		}
		if !names(leaf, Identity(troupe, e.Node)) {
			return out, fmt.Errorf("a leave for %s is signed by another node", e.Node)
		}
		pub = leaf.PublicKey
	case Renewer:
		key, err := verifyRenewer(out, troupe, root, at)
		if err != nil {
			return out, err
		}
		pub = key
	case Revoke:
		if e.Door != "" || e.Owner != "" || len(out.chain) != 0 {
			return out, errors.New("a revoke is signed by the root alone, and has no door")
		}
		pub = root.PublicKey
	default:
		return out, fmt.Errorf("kind %q", e.Kind)
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(ed, e.message(), e.Sig) {
		return out, errors.New("bad signature")
	}
	return out, nil
}

const maxOwner = 256

func ValidOwner(owner string) error {
	kind, id, ok := strings.Cut(owner, ":")
	if !ok || kind == "" || id == "" || len(owner) > maxOwner || strings.ContainsAny(owner, " \t\r\n") {
		return fmt.Errorf("owner %q: a join names the principal that owns the node, as kind:id", owner)
	}
	return nil
}

func chainsTo(c, inter, root *x509.Certificate, at time.Time) error {
	roots := x509.NewCertPool()
	roots.AddCert(root)
	opts := x509.VerifyOptions{Roots: roots, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}
	if inter != nil {
		opts.Intermediates = x509.NewCertPool()
		opts.Intermediates.AddCert(inter)
	}
	_, err := c.Verify(opts)
	return err
}

func names(c *x509.Certificate, identity string) bool {
	for _, u := range c.URIs {
		if u.String() == identity {
			return true
		}
	}
	return false
}

type Member struct {
	Node       string
	Door       string
	Owner      string
	AdmittedBy string
	Since      time.Time
	SignsUntil time.Time
	Vouched    bool
}

type View struct {
	Members    map[string]Member
	Renewers   map[string][]string
	Gone       map[string]Kind
	Policy     *Entry
	Rivals     []Entry
	Voided     []Entry
	Refused    []Entry
	APIKeys    map[string]APIKeyRecord
	Digest     uint64
	KeysDigest uint64
}

func (v View) Signers(now time.Time) []string {
	var out []string
	for n, m := range v.Members {
		if now.Before(m.SignsUntil) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

type Ledger struct {
	troupe  string
	root    *x509.Certificate
	now     func() time.Time
	entries map[string]verified
	counts  map[Kind]int
	view    View
}

func NewLedger(troupe string, root *x509.Certificate) *Ledger {
	l := &Ledger{troupe: troupe, root: root, now: time.Now, entries: map[string]verified{}, counts: map[Kind]int{}}
	l.rebuild()
	return l
}

func (l *Ledger) Troupe() string { return l.troupe }

func (l *Ledger) Merge(es ...Entry) ([]Entry, error) {
	var errs []error
	var added []Entry
	var parked []verified
	for _, e := range es {
		id := e.ID()
		if _, ok := l.entries[id]; ok {
			continue
		}
		if (e.Kind == Join || e.Kind == Policy) && len(l.entries)-l.counts[APIKey]-l.counts[APIKeyRevoke] >= MaxEntries {
			errs = append(errs, fmt.Errorf("troupe %s holds %d entries, its limit for joins and policies", l.troupe, MaxEntries))
			continue
		}
		if isKeyKind(e.Kind) && l.counts[e.Kind] >= MaxAPIKeys {
			errs = append(errs, fmt.Errorf("troupe %s holds %d %s entries, its limit", l.troupe, MaxAPIKeys, e.Kind))
			continue
		}
		if Prunable(e, l.now()) {
			continue
		}
		v, err := verify(e, l.troupe, l.root)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", e.Kind, e.Node, err))
			continue
		}
		if e.Kind == Policy && !e.RootSigned() {
			parked = append(parked, v)
			continue
		}
		l.entries[id] = v
		l.count(e.Kind, 1)
		added = append(added, e)
	}
	for progress := true; progress && len(parked) > 0; {
		progress = false
		rest := parked[:0]
		for _, v := range parked {
			if !l.namedSetter(v.Node) {
				rest = append(rest, v)
				continue
			}
			if _, ok := l.entries[v.ID()]; !ok {
				l.entries[v.ID()] = v
				added = append(added, v.Entry)
			}
			progress = true
		}
		parked = rest
	}
	for _, v := range parked {
		errs = append(errs, fmt.Errorf("policy version %d from %s: no policy here names that node a setter", v.Version, v.Node))
	}
	if len(added) > 0 {
		l.rebuild()
	}
	return added, errors.Join(errs...)
}

func (l *Ledger) namedSetter(node string) bool {
	for _, v := range l.entries {
		if v.Kind != Policy {
			continue
		}
		if setters, _ := PolicySetters(v.Body); slices.Contains(setters, node) {
			return true
		}
	}
	return false
}

func (l *Ledger) Entries() []Entry {
	ids := slices.Sorted(maps.Keys(l.entries))
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = l.entries[id].Entry
	}
	return out
}

func (l *Ledger) View() View { return l.view }

func (l *Ledger) rebuild() {
	ids := slices.Sorted(maps.Keys(l.entries))
	v := View{Members: map[string]Member{}, Renewers: map[string][]string{}, Gone: map[string]Kind{}}
	for _, id := range ids {
		if e := l.entries[id]; e.Kind == Revoke {
			v.Gone[e.Node] = Revoke
		}
	}
	for _, id := range ids {
		e := l.entries[id]
		if e.Kind == Renewer && v.Gone[e.Renews] != Revoke && !slices.Contains(v.Renewers[e.Node], e.Renews) {
			v.Renewers[e.Node] = append(v.Renewers[e.Node], e.Renews)
		}
	}
	for n := range v.Renewers {
		slices.Sort(v.Renewers[n])
	}
	for _, id := range ids {
		e := l.entries[id]
		if e.Kind == Leave && v.Gone[e.Node] == "" && v.MayIssue(e.Node, IntermediateNode(e.chain[1])) {
			v.Gone[e.Node] = Leave
		}
	}
	vouched := map[string]bool{}
	for _, id := range ids {
		if e := l.entries[id]; e.Kind == Join && v.MayIssue(e.Node, e.signer()) {
			vouched[e.Node] = true
		}
	}
	latest := map[string]int64{}
	for _, id := range ids {
		e := l.entries[id]
		if e.Kind != Join || v.Gone[e.Node] != "" || v.Gone[e.signer()] == Revoke {
			continue
		}
		if vouched[e.Node] && !v.MayIssue(e.Node, e.signer()) {
			continue
		}
		m, seen := v.Members[e.Node]
		m.Vouched = vouched[e.Node]
		if !seen || e.Time().Before(m.Since) {
			m.Node, m.AdmittedBy, m.Since = e.Node, e.signer(), e.Time()
		}
		if e.At >= latest[e.Node] {
			latest[e.Node], m.Door, m.Owner = e.At, e.Door, e.Owner
		}
		if e.signer() == e.Node && e.chain[0].NotAfter.After(m.SignsUntil) {
			m.SignsUntil = e.chain[0].NotAfter
		}
		v.Members[e.Node] = m
	}
	var policies []Entry
	for _, id := range ids {
		e := l.entries[id]
		switch {
		case e.Kind != Policy:
		case !e.RootSigned() && v.Gone[e.Node] == Revoke:
			v.Voided = append(v.Voided, e.Entry)
		default:
			policies = append(policies, e.Entry)
		}
	}
	w := walkPolicies(policies)
	v.Policy, v.Rivals, v.Refused = w.adopted, w.rivals, w.refused
	v.APIKeys = l.apiKeyView(ids, v.Gone, v.Policy)
	if len(ids) > 0 {
		h, k := sha256.New(), sha256.New()
		if v.Policy != nil {
			k.Write([]byte(v.Policy.ID()))
		}
		for _, id := range ids {
			h.Write([]byte(id))
			if kind := l.entries[id].Kind; isKeyKind(kind) || kind == Revoke {
				k.Write([]byte(id))
			}
		}
		v.Digest = max(binary.BigEndian.Uint64(h.Sum(nil)), 1)
		v.KeysDigest = binary.BigEndian.Uint64(k.Sum(nil))
	}
	l.view = v
}

func (l *Ledger) count(k Kind, n int) { l.counts[k] += n }

func Encode(es []Entry) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(es))
	for _, e := range es {
		raw, _ := json.Marshal(e)
		out = append(out, raw)
	}
	return out
}

func Decode(raw []json.RawMessage) ([]Entry, error) {
	if limit := MaxEntries + 2*MaxAPIKeys; len(raw) > limit {
		return nil, fmt.Errorf("%d entries, more than a troupe holds (%d)", len(raw), limit)
	}
	out := make([]Entry, 0, len(raw))
	for i, r := range raw {
		var e Entry
		if err := json.Unmarshal(r, &e); err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		out = append(out, e)
	}
	return out, nil
}
