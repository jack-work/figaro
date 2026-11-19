package approvals

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed page.html page.css
var assets embed.FS

var page = template.Must(template.ParseFS(assets, "page.html"))

type Server struct {
	Decider *Decider
	Filings []string
	Admin   string
	Origin  string
	key     []byte
}

func NewServer(d *Decider, filings []string, admin, origin string) *Server {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Server{Decider: d, Filings: filings, Admin: admin, Origin: origin, key: key}
}

type view struct {
	Rows    []row
	Detail  *row
	Message string
}

type row struct {
	Pending
	Version      uint64
	Lines        []shownLine
	Inputs       map[string]string
	Hash         string
	Applied      bool
	AtSuccession bool
	Token        string
	Link         string
}

type shownLine struct {
	Op, Key, Current, New string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.list)
	mux.HandleFunc("GET /r/{filing}/{id}", s.detail)
	mux.HandleFunc("POST /approve", s.decide(true))
	mux.HandleFunc("POST /deny", s.decide(false))
	mux.HandleFunc("GET /page.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		b, _ := assets.ReadFile("page.css")
		_, _ = w.Write(b)
	})
	return s.gate(mux)
}

func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if s.Admin == "" || r.Header.Get("Remote-User") != s.Admin {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && (s.Origin == "" || r.Header.Get("Origin") != s.Origin) {
			http.Error(w, "cross-origin post refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rows(ctx context.Context) ([]row, error) {
	pending, err := List(ctx, s.Decider.Forms, s.Filings, s.Decider.Now())
	if err != nil {
		return nil, err
	}
	out := make([]row, 0, len(pending))
	for _, p := range pending {
		_, version, _ := s.Decider.Forms.Read(ctx, p.Filing)
		r := row{Pending: p, Version: version,
			Link: "/r/" + url.PathEscape(p.Filing) + "/" + url.PathEscape(p.ID)}
		if p.Plan != nil {
			r.Hash, r.Applied, r.Inputs = p.Plan.Hash, p.Plan.Applied, p.Plan.Inputs
			r.AtSuccession = TakesEffectAtSuccession(p.Plan.Verb)
			for _, l := range p.Plan.Lines {
				r.Lines = append(r.Lines, shownLine{Op: "set", Key: l.Key, Current: string(l.Current), New: string(l.New)})
			}
		}
		r.Lines = append(r.Lines, shownLine{Op: "remove", Key: ElicitKey + "." + p.ID})
		r.Token = s.token(p.Filing, p.ID, r.Hash, version)
		out = append(out, r)
	}
	return out, nil
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rows(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = page.Execute(w, view{Rows: rows})
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rows(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for i := range rows {
		if rows[i].Filing == r.PathValue("filing") && rows[i].ID == r.PathValue("id") {
			_ = page.Execute(w, view{Detail: &rows[i]})
			return
		}
	}
	_ = page.Execute(w, view{Message: "No such request: it was decided, withdrawn, or never filed."})
}

func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		filing, id, hash := r.PostForm.Get("filing"), r.PostForm.Get("id"), r.PostForm.Get("hash")
		version, err := strconv.ParseUint(r.PostForm.Get("version"), 10, 64)
		if err != nil || !hmac.Equal([]byte(r.PostForm.Get("token")), []byte(s.token(filing, id, hash, version))) {
			http.Error(w, "stale or forged form token: reload the page", http.StatusForbidden)
			return
		}
		if !s.knows(filing) {
			http.Error(w, "unknown filing form", http.StatusBadRequest)
			return
		}
		dec := Decision{Approver: s.Admin, Filing: filing, ID: id, Hash: hash, Version: version}
		var result string
		if approve {
			result, err = s.Decider.Approve(r.Context(), dec)
		} else {
			result, err = s.Decider.Deny(r.Context(), dec)
		}
		msg := result
		if err != nil {
			msg = strings.TrimPrefix(result+"; refused: "+err.Error(), "; ")
			w.WriteHeader(http.StatusConflict)
		}
		_ = page.Execute(w, view{Message: msg})
	}
}

func (s *Server) knows(filing string) bool {
	for _, f := range s.Filings {
		if f == filing {
			return true
		}
	}
	return false
}

func (s *Server) token(filing, id, hash string, version uint64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(filing + "\x00" + id + "\x00" + hash + "\x00" + strconv.FormatUint(version, 10)))
	return hex.EncodeToString(m.Sum(nil))
}
