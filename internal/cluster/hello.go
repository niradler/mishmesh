package cluster

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

const helloNonceBytes = 16

var ErrReplayedHello = errors.New("cluster: hello nonce already used")

type Hello struct {
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"ts"`
	MAC       string `json:"mac"`
}

func (h Hello) computeMAC(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("mishmesh-relay-hello\x00"))
	m.Write([]byte(h.Nonce))
	m.Write([]byte{0})
	m.Write([]byte(strconv.FormatInt(h.Timestamp, 10)))
	return m.Sum(nil)
}

func NewHello(secret []byte, now time.Time) (Hello, error) {
	var raw [helloNonceBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Hello{}, fmt.Errorf("cluster: hello nonce: %w", err)
	}
	h := Hello{Nonce: hex.EncodeToString(raw[:]), Timestamp: now.Unix()}
	h.MAC = hex.EncodeToString(h.computeMAC(secret))
	return h, nil
}

func (h Hello) Verify(secret []byte, now time.Time) error {
	got, err := hex.DecodeString(h.MAC)
	if err != nil || h.Nonce == "" {
		return ErrBadSignature
	}
	if !hmac.Equal(got, h.computeMAC(secret)) {
		return ErrBadSignature
	}
	skew := now.Sub(time.Unix(h.Timestamp, 0))
	if skew > MaxClockSkew || skew < -MaxClockSkew {
		return ErrStaleHeader
	}
	return nil
}

func writeHello(w io.Writer, h Hello) error {
	body, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("cluster: marshal hello: %w", err)
	}
	frame := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(frame, uint32(len(body)))
	copy(frame[4:], body)
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("cluster: write hello: %w", err)
	}
	return nil
}

func readHello(r io.Reader) (Hello, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return Hello{}, fmt.Errorf("cluster: read hello length: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > MaxHeaderBytes {
		return Hello{}, ErrHeaderTooBig
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Hello{}, fmt.Errorf("cluster: read hello: %w", err)
	}
	var h Hello
	if err := json.Unmarshal(body, &h); err != nil {
		return Hello{}, fmt.Errorf("cluster: decode hello: %w", err)
	}
	return h, nil
}

type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newNonceCache() *nonceCache {
	return &nonceCache{seen: make(map[string]time.Time)}
}

func (n *nonceCache) use(nonce string, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, exp := range n.seen {
		if now.After(exp) {
			delete(n.seen, k)
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return ErrReplayedHello
	}
	n.seen[nonce] = now.Add(2 * MaxClockSkew)
	return nil
}
