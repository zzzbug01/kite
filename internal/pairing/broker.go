package pairing

import (
	"errors"
	"sync"
	"time"
)

// Defaults for a personal relay. Tests override them.
const (
	defaultCreateIPLimit    = 10
	defaultCreateAgentLimit = 10
	defaultRedeemIPLimit    = 30
	defaultLimitWindow      = 10 * time.Minute
	defaultMaxCodesPerAgent = 3
	defaultMaxCodes         = 100
)

// Config tunes the in-memory pairing-code broker.
type Config struct {
	Now              func() time.Time
	TTL              time.Duration
	MaxTTL           time.Duration
	CreateIPLimit    int
	CreateAgentLimit int
	RedeemIPLimit    int
	LimitWindow      time.Duration
	MaxCodesPerAgent int
	MaxCodes         int
}

// Issued is what a successful redeem returns. It does not include the code.
type Issued struct {
	Agent     string
	SSHUser   string
	ClientURL string
	Expires   time.Time
}

// Broker holds outstanding pairing codes. Codes live only in memory; a
// relay restart drops them. The registration key is never stored here.
type Broker struct {
	mu       sync.Mutex
	now      func() time.Time
	ttl      time.Duration
	maxTTL   time.Duration
	lim      *Limiter
	createIP int
	createAg int
	redeemIP int
	window   time.Duration
	maxAgent int
	maxAll   int
	codes    []*pending
}

type pending struct {
	hash      []byte
	agent     string
	sshUser   string
	clientURL string
	expires   time.Time
	used      bool
	inflight  bool
}

// NewBroker builds a broker. Zero Config fields take the defaults.
func NewBroker(cfg Config) *Broker {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	maxTTL := cfg.MaxTTL
	if maxTTL <= 0 {
		maxTTL = MaxTTL
	}
	window := cfg.LimitWindow
	if window <= 0 {
		window = defaultLimitWindow
	}
	b := &Broker{
		now:      now,
		ttl:      ttl,
		maxTTL:   maxTTL,
		lim:      newLimiter(),
		createIP: positive(cfg.CreateIPLimit, defaultCreateIPLimit),
		createAg: positive(cfg.CreateAgentLimit, defaultCreateAgentLimit),
		redeemIP: positive(cfg.RedeemIPLimit, defaultRedeemIPLimit),
		window:   window,
		maxAgent: positive(cfg.MaxCodesPerAgent, defaultMaxCodesPerAgent),
		maxAll:   positive(cfg.MaxCodes, defaultMaxCodes),
	}
	return b
}

func positive(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// Create mints a code for agent. ip and agent are rate-limit keys, not secrets.
// The returned string is the only copy of the raw code.
func (b *Broker) Create(ip, agent, sshUser, clientURL string, ttl time.Duration) (string, time.Time, error) {
	now := b.now()
	if !b.lim.AllowAll(now,
		Rule{Key: "create:ip:" + ip, Limit: b.createIP, Window: b.window},
		Rule{Key: "create:agent:" + agent, Limit: b.createAg, Window: b.window},
	) {
		return "", time.Time{}, ErrRateLimited
	}
	if ttl <= 0 {
		ttl = b.ttl
	}
	if ttl > b.maxTTL {
		ttl = b.maxTTL
	}
	norm, err := newCode()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := now.Add(ttl)
	item := &pending{
		hash:      hashCode(norm),
		agent:     agent,
		sshUser:   sshUser,
		clientURL: clientURL,
		expires:   exp,
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweep(now)
	nAgent := 0
	for _, c := range b.codes {
		if !c.used && c.agent == agent {
			nAgent++
		}
	}
	if nAgent >= b.maxAgent || len(b.codes) >= b.maxAll {
		return "", time.Time{}, ErrTooManyCodes
	}
	b.codes = append(b.codes, item)
	return FormatCode(norm), exp, nil
}

// TouchRedeem records a redeem attempt that failed before the code was
// checked (for example a public key the relay will not accept). Successful
// and well-formed attempts are counted by Redeem itself.
func (b *Broker) TouchRedeem(ip string) error {
	if !b.lim.AllowAll(b.now(), Rule{Key: "redeem:ip:" + ip, Limit: b.redeemIP, Window: b.window}) {
		return ErrRateLimited
	}
	return nil
}

// ErrCodeBusy means another redeem of this code is in progress. It is not used.
var ErrCodeBusy = errors.New("pairing code is in use; retry")

// Begin reserves a code without consuming it. Call done(true) after the
// agent accepts the key, or done(false) to leave the code usable until it
// expires. A second Begin while one is in flight returns ErrCodeBusy.
func (b *Broker) Begin(ip, code string) (Issued, func(success bool), error) {
	now := b.now()
	if !b.lim.AllowAll(now, Rule{Key: "redeem:ip:" + ip, Limit: b.redeemIP, Window: b.window}) {
		return Issued{}, nil, ErrRateLimited
	}
	norm, err := NormalizeCode(code)
	if err != nil {
		_ = matchHash(nil, hashCode("AAAAAAAA"))
		return Issued{}, nil, ErrCodeInvalid
	}
	sum := hashCode(norm)
	b.mu.Lock()
	defer b.mu.Unlock()
	hashes := make([][]byte, len(b.codes))
	for i, c := range b.codes {
		hashes[i] = c.hash
	}
	idx := matchHash(hashes, sum)
	if idx < 0 {
		b.sweep(now)
		return Issued{}, nil, ErrCodeInvalid
	}
	c := b.codes[idx]
	if c.used {
		b.sweep(now)
		return Issued{}, nil, ErrCodeUsed
	}
	if !now.Before(c.expires) {
		b.sweep(now)
		return Issued{}, nil, ErrCodeExpired
	}
	if c.inflight {
		return Issued{}, nil, ErrCodeBusy
	}
	c.inflight = true
	issued := Issued{Agent: c.agent, SSHUser: c.sshUser, ClientURL: c.clientURL, Expires: c.expires}
	done := func(success bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if success {
			c.used = true
		}
		c.inflight = false
	}
	return issued, done, nil
}

// Redeem consumes code. A second call with the same code returns ErrCodeUsed.
// A wrong code returns ErrCodeInvalid and does not affect other codes.
func (b *Broker) Redeem(ip, code string) (Issued, error) {
	issued, done, err := b.Begin(ip, code)
	if err != nil {
		return Issued{}, err
	}
	done(true)
	return issued, nil
}

// Outstanding is the number of unexpired, unused codes. Tests use it.
func (b *Broker) Outstanding() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweep(b.now())
	n := 0
	for _, c := range b.codes {
		if !c.used {
			n++
		}
	}
	return n
}

func (b *Broker) sweep(now time.Time) {
	kept := b.codes[:0]
	for _, c := range b.codes {
		if now.Before(c.expires) {
			kept = append(kept, c)
		}
	}
	b.codes = kept
}
