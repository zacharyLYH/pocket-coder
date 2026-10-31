package sync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// FrameSize is the plaintext chunk sealed into one AES-GCM frame.
const FrameSize = 64 << 10

const (
	keyInfo   = "pcoder-sync v1"
	maxFrame  = FrameSize + 16 // ciphertext + GCM tag
	frameHead = 4              // big-endian ciphertext length
)

// ErrBadFrame means a frame failed authentication: wrong code, wrong
// session, wrong direction, wrong sequence, or tampering.
var ErrBadFrame = errors.New("sync payload failed authentication")

// DeriveKey returns the 32-byte AES key for a sync code and session nonce.
// The code is the only shared secret; nothing else key material lives on
// either side.
func DeriveKey(code, sessionNonce string) ([]byte, error) {
	key := make([]byte, 32)
	r := hkdf.New(sha256.New, []byte(code), []byte(sessionNonce), []byte(keyInfo))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("derive sync key: %w", err)
	}
	return key, nil
}

// NewNonce returns a random lowercase hex nonce for a session.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// AAD is the associated data bound into every frame. It ties a payload to
// one session, project, branch, commit, and direction, so a frame from any
// other sync is rejected even with a valid tag.
type AAD struct {
	Session   string
	Project   string
	Branch    string
	Commit    string
	Direction string
}

func (a AAD) bytes(seq uint64) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d",
		a.Session, a.Project, a.Branch, a.Commit, a.Direction, seq))
}

func frameNonce(seq uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], seq)
	return n
}

// NewFrameWriter seals plaintext into length-prefixed AES-256-GCM frames.
// One Write call is one frame.
func NewFrameWriter(w io.Writer, key []byte, aad AAD) (*FrameWriter, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &FrameWriter{w: w, gcm: gcm, aad: aad}, nil
}

// FrameWriter writes encrypted frames.
type FrameWriter struct {
	w   io.Writer
	gcm cipher.AEAD
	aad AAD
	seq uint64
}

// Write seals p as the next frame.
func (f *FrameWriter) Write(p []byte) (int, error) {
	if len(p) > FrameSize {
		return 0, fmt.Errorf("sync frame too large: %d bytes", len(p))
	}
	ct := f.gcm.Seal(nil, frameNonce(f.seq), p, f.aad.bytes(f.seq))
	var head [frameHead]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(ct)))
	if _, err := f.w.Write(head[:]); err != nil {
		return 0, err
	}
	if _, err := f.w.Write(ct); err != nil {
		return 0, err
	}
	f.seq++
	return len(p), nil
}

// NewFrameReader decrypts frames written by NewFrameWriter.
func NewFrameReader(r io.Reader, key []byte, aad AAD) (*FrameReader, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &FrameReader{r: r, gcm: gcm, aad: aad}, nil
}

// FrameReader yields decrypted plaintext, one frame at a time.
type FrameReader struct {
	r    io.Reader
	gcm  cipher.AEAD
	aad  AAD
	seq  uint64
	buf  []byte
	done bool
}

// Read fills p from the current frame, decrypting the next one when empty.
func (f *FrameReader) Read(p []byte) (int, error) {
	for len(f.buf) == 0 {
		if f.done {
			return 0, io.EOF
		}
		if err := f.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *FrameReader) next() error {
	var head [frameHead]byte
	if _, err := io.ReadFull(f.r, head[:]); err != nil {
		if errors.Is(err, io.EOF) {
			f.done = true
			return io.EOF
		}
		return fmt.Errorf("read sync frame: %w", err)
	}
	size := int(binary.BigEndian.Uint32(head[:]))
	if size < f.gcm.Overhead() || size > maxFrame {
		return ErrBadFrame
	}
	ct := make([]byte, size)
	if _, err := io.ReadFull(f.r, ct); err != nil {
		return fmt.Errorf("read sync frame: %w", err)
	}
	pt, err := f.gcm.Open(nil, frameNonce(f.seq), ct, f.aad.bytes(f.seq))
	if err != nil {
		return ErrBadFrame
	}
	f.seq++
	f.buf = pt
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	return cipher.NewGCM(block)
}
