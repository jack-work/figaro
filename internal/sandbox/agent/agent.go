package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jack-work/jkrpc"
)

const (
	Protocol = 1
	ErrCode  = -32060

	MethodHello     = "agent.hello"
	MethodClasses   = "agent.classes"
	MethodSandboxes = "agent.sandboxes"
	MethodLease     = "agent.lease"
	MethodReset     = "agent.reset"
	MethodCordon    = "agent.cordon"

	Free      = "free"
	Leased    = "leased"
	Resetting = "resetting"
	Cordoned  = "cordoned"

	Job          = "job"
	Conversation = "conversation"

	UnknownClass   = "unknown_class"
	NoFreeSandbox  = "no_free_sandbox"
	GenerationUsed = "generation_used"
	UnknownSandbox = "unknown_sandbox"
	WrongState     = "wrong_state"
	ResetFailed    = "reset_failed"
	InvalidParams  = "invalid_params"

	MaxReason = 200

	dialTimeout  = 5 * time.Second
	ResetTimeout = 120 * time.Second
)

var (
	nameShape   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	leaseSuffix = regexp.MustCompile(`-g[0-9]+$`)
	figaroShape = regexp.MustCompile(`^[0-9a-f]{32}$`)
	nonceShape  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func ValidName(s string) bool { return nameShape.MatchString(s) && !leaseSuffix.MatchString(s) }

func ValidFigaro(s string) bool { return figaroShape.MatchString(s) }

func ValidNonce(s string) bool { return nonceShape.MatchString(s) }

func ValidWorkload(s string) bool { return s == Job || s == Conversation }

type Hello struct {
	Protocol int    `json:"protocol"`
	Node     string `json:"node"`
}

type Class struct {
	Name      string   `json:"name"`
	Workload  string   `json:"workload"`
	Sandboxes []string `json:"sandboxes"`
	Writable  []string `json:"writable"`
	Egress    []string `json:"egress"`
}

type Sandbox struct {
	Name       string `json:"name"`
	Class      string `json:"class"`
	State      string `json:"state"`
	Generation uint64 `json:"generation,omitempty"`
	Figaro     string `json:"figaro,omitempty"`
	Exec       string `json:"exec"`
}

type LeaseRequest struct {
	Class      string `json:"class"`
	Generation uint64 `json:"generation"`
	Figaro     string `json:"figaro"`
}

type Lease struct {
	Sandbox    string `json:"sandbox"`
	Generation uint64 `json:"generation"`
	Exec       string `json:"exec"`
}

type ResetRequest struct {
	Sandbox    string `json:"sandbox"`
	Generation uint64 `json:"generation"`
	Nonce      string `json:"nonce"`
}

type Receipt struct {
	Sandbox    string    `json:"sandbox"`
	Generation uint64    `json:"generation"`
	Nonce      string    `json:"nonce"`
	At         time.Time `json:"at"`
}

type CordonRequest struct {
	Sandbox    string `json:"sandbox"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason"`
}

type classesResp struct {
	Classes []Class `json:"classes"`
}

type sandboxesResp struct {
	Sandboxes []Sandbox `json:"sandboxes"`
}

type Refusal struct {
	Reason  string
	Message string
	Data    map[string]json.RawMessage
}

func (r *Refusal) Error() string {
	if r.Message != "" {
		return fmt.Sprintf("node agent refused: %s (%s)", r.Reason, r.Message)
	}
	return "node agent refused: " + r.Reason
}

func RefusedFor(err error, reason string) bool {
	var r *Refusal
	return errors.As(err, &r) && r.Reason == reason
}

func SocketPath(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	switch {
	case err != nil:
		return "", fmt.Errorf("endpoint %q: %w", endpoint, err)
	case u.Scheme != "unix":
		return "", fmt.Errorf("endpoint %q: want unix://<absolute path>; no other transport exists yet", endpoint)
	case u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("endpoint %q: want unix://<absolute path> and nothing else", endpoint)
	case !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path:
		return "", fmt.Errorf("endpoint %q: want a clean absolute path", endpoint)
	}
	return u.Path, nil
}

type Client struct {
	endpoint string
	rpc      *jkrpc.Client
	node     string
}

func Dial(ctx context.Context, endpoint string) (*Client, error) {
	path, err := SocketPath(endpoint)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: dialTimeout}
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("node agent %s: %w", endpoint, err)
	}
	c := &Client{endpoint: endpoint, rpc: jkrpc.NewClient(jkrpc.NewConn(nc), nil)}
	var h Hello
	if err := c.call(ctx, MethodHello, struct{}{}, &h); err != nil {
		c.Close()
		return nil, err
	}
	if h.Protocol != Protocol {
		c.Close()
		return nil, fmt.Errorf("node agent %s speaks protocol %d; this figaro speaks %d", endpoint, h.Protocol, Protocol)
	}
	c.node = h.Node
	return c, nil
}

func (c *Client) Node() string { return c.node }

func (c *Client) Close() error { return c.rpc.Close() }

func (c *Client) Done() <-chan struct{} { return c.rpc.Done() }

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	err := c.rpc.Call(ctx, method, params, result)
	if err == nil {
		return nil
	}
	var je *jkrpc.Error
	if !errors.As(err, &je) {
		return fmt.Errorf("node agent %s: %s: %w", c.endpoint, method, err)
	}
	if je.Code != ErrCode {
		return fmt.Errorf("node agent %s: %s: %w", c.endpoint, method, err)
	}
	r := &Refusal{Message: je.Message}
	if len(je.Data) > 0 {
		_ = json.Unmarshal(je.Data, &r.Data)
	}
	if raw, ok := r.Data["reason"]; ok {
		_ = json.Unmarshal(raw, &r.Reason)
	}
	if r.Reason == "" {
		return fmt.Errorf("node agent %s: %s: a refusal with no reason: %w", c.endpoint, method, err)
	}
	return r
}

func (c *Client) Classes(ctx context.Context) ([]Class, error) {
	var out classesResp
	if err := c.call(ctx, MethodClasses, struct{}{}, &out); err != nil {
		return nil, err
	}
	for _, cl := range out.Classes {
		if !ValidName(cl.Name) || !ValidWorkload(cl.Workload) {
			return nil, fmt.Errorf("node agent %s: class %q with workload %q is out of shape", c.endpoint, cl.Name, cl.Workload)
		}
		for _, s := range cl.Sandboxes {
			if !ValidName(s) {
				return nil, fmt.Errorf("node agent %s: class %s names sandbox %q, out of shape", c.endpoint, cl.Name, s)
			}
		}
	}
	return out.Classes, nil
}

func (c *Client) Sandboxes(ctx context.Context) ([]Sandbox, error) {
	var out sandboxesResp
	if err := c.call(ctx, MethodSandboxes, struct{}{}, &out); err != nil {
		return nil, err
	}
	for _, s := range out.Sandboxes {
		if !ValidName(s.Name) {
			return nil, fmt.Errorf("node agent %s: sandbox %q is out of shape", c.endpoint, s.Name)
		}
		if _, err := SocketPath(s.Exec); err != nil {
			return nil, fmt.Errorf("node agent %s: sandbox %s: %w", c.endpoint, s.Name, err)
		}
	}
	return out.Sandboxes, nil
}

func (c *Client) Lease(ctx context.Context, req LeaseRequest) (Lease, error) {
	if !ValidName(req.Class) || req.Generation == 0 || !ValidFigaro(req.Figaro) {
		return Lease{}, fmt.Errorf("lease %s at generation %d for %q: out of shape", req.Class, req.Generation, req.Figaro)
	}
	var out Lease
	if err := c.call(ctx, MethodLease, req, &out); err != nil {
		return Lease{}, err
	}
	if !ValidName(out.Sandbox) || out.Generation != req.Generation {
		return Lease{}, fmt.Errorf("node agent %s answered lease %s/%d with sandbox %q at generation %d", c.endpoint, req.Class, req.Generation, out.Sandbox, out.Generation)
	}
	if _, err := SocketPath(out.Exec); err != nil {
		return Lease{}, fmt.Errorf("node agent %s: lease of %s: %w", c.endpoint, out.Sandbox, err)
	}
	return out, nil
}

func (c *Client) Reset(ctx context.Context, req ResetRequest) (Receipt, error) {
	if !ValidName(req.Sandbox) || req.Generation == 0 || !ValidNonce(req.Nonce) {
		return Receipt{}, fmt.Errorf("reset %s at generation %d: out of shape", req.Sandbox, req.Generation)
	}
	ctx, cancel := context.WithTimeout(ctx, ResetTimeout)
	defer cancel()
	var out Receipt
	if err := c.call(ctx, MethodReset, req, &out); err != nil {
		return Receipt{}, err
	}
	if out.Sandbox != req.Sandbox || out.Generation != req.Generation || out.Nonce != req.Nonce || out.At.IsZero() {
		return Receipt{}, fmt.Errorf("node agent %s: the receipt for %s/%d does not match the reset asked for", c.endpoint, req.Sandbox, req.Generation)
	}
	return out, nil
}

func (c *Client) Cordon(ctx context.Context, req CordonRequest) error {
	if len(req.Reason) > MaxReason {
		req.Reason = strings.ToValidUTF8(req.Reason[:MaxReason], "")
	}
	if !ValidName(req.Sandbox) {
		return fmt.Errorf("cordon %q: out of shape", req.Sandbox)
	}
	return c.call(ctx, MethodCordon, req, &struct{}{})
}
