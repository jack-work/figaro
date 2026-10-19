package apikey

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"

	"github.com/jack-work/jkauthz"
)

const (
	Prefix     = "fgk_"
	idLen      = 10
	secretLen  = 32
	hashDomain = "figaro-api-key\x00"

	DefaultTTL = 90 * 24 * time.Hour
	MaxTTL     = 365 * 24 * time.Hour
	MaxName    = 64
	HashSize   = sha256.Size
)

var (
	encoding  = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	secretB32 = encoding.EncodedLen(secretLen)
	crcB32    = encoding.EncodedLen(4)
	wireLen   = len(Prefix) + idLen + 1 + secretB32 + 1 + crcB32

	ErrMalformed = errors.New("not a figaro api key")
)

type Key struct {
	ID   string
	Hash [HashSize]byte
}

func Is(token string) bool { return strings.HasPrefix(token, Prefix) }

func New(rand io.Reader) (string, Key, error) {
	raw := make([]byte, 7+secretLen)
	if _, err := io.ReadFull(rand, raw); err != nil {
		return "", Key{}, err
	}
	id := encoding.EncodeToString(raw[:7])[:idLen]
	secret := raw[7:]
	body := Prefix + id + "_" + encoding.EncodeToString(secret)
	wire := body + "_" + checksum(body)
	return wire, Key{ID: id, Hash: hash(id, secret)}, nil
}

func Parse(wire string) (Key, error) {
	if len(wire) != wireLen || !Is(wire) {
		return Key{}, ErrMalformed
	}
	rest := wire[len(Prefix):]
	id, rest, ok1 := strings.Cut(rest, "_")
	enc, sum, ok2 := strings.Cut(rest, "_")
	if !ok1 || !ok2 || ValidID(id) != nil || len(enc) != secretB32 || len(sum) != crcB32 {
		return Key{}, ErrMalformed
	}
	if checksum(wire[:len(wire)-crcB32-1]) != sum {
		return Key{}, ErrMalformed
	}
	secret, err := encoding.DecodeString(enc)
	if err != nil || len(secret) != secretLen {
		return Key{}, ErrMalformed
	}
	return Key{ID: id, Hash: hash(id, secret)}, nil
}

func (k Key) Matches(stored []byte) bool {
	return len(stored) == HashSize && subtle.ConstantTimeCompare(k.Hash[:], stored) == 1
}

func ValidID(id string) error {
	if len(id) != idLen {
		return fmt.Errorf("api key id %q: want %d characters", id, idLen)
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return fmt.Errorf("api key id %q: want base32 (a-z, 2-7)", id)
		}
	}
	return nil
}

func hash(id string, secret []byte) [HashSize]byte {
	h := sha256.New()
	h.Write([]byte(hashDomain))
	h.Write([]byte(id))
	h.Write(secret)
	var out [HashSize]byte
	h.Sum(out[:0])
	return out
}

func checksum(body string) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], crc32.ChecksumIEEE([]byte(body)))
	return encoding.EncodeToString(b[:])
}

func Scope(troupe string) string { return "troupe:" + troupe }

func Principal(kind jkauthz.Kind, name, troupe string) (jkauthz.Principal, error) {
	p := jkauthz.Principal{Kind: kind, ID: name + "@" + Scope(troupe)}
	return p, CheckPrincipal(p, troupe)
}

func CheckPrincipal(p jkauthz.Principal, troupe string) error {
	if p.Kind != jkauthz.KindUser && p.Kind != "service" {
		return fmt.Errorf("api key principal %s: an api key names a user or a service", p)
	}
	if p.Node != "" {
		return fmt.Errorf("api key principal %s: names no node", p)
	}
	name, scope, ok := strings.Cut(p.ID, "@")
	if !ok || scope != Scope(troupe) {
		return fmt.Errorf("api key principal %s: want <name>@%s", p, Scope(troupe))
	}
	if name == "" || len(name) > MaxName || strings.ContainsAny(name, "@:/* \t\r\n") {
		return fmt.Errorf("api key principal %s: name %q is 1 to %d characters without @ : / * or space", p, name, MaxName)
	}
	return nil
}

func ParsePrincipal(s, troupe string) (jkauthz.Principal, error) {
	p, err := jkauthz.ParsePrincipal(s)
	if err != nil {
		return jkauthz.Principal{}, err
	}
	return p, CheckPrincipal(p, troupe)
}
