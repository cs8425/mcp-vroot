package main

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// Checksums contains streaming computed hashes.
type Checksums struct {
	SHA256Hex string
	SHA384Hex string
	SHA512Hex string

	// SRI integrity formats.
	SHA256Integrity string
	SHA384Integrity string
	SHA512Integrity string
}

// String returns a compact hex summary.
func (c Checksums) String() string {
	return fmt.Sprintf(
		"sha256=%s sha384=%s sha512=%s",
		c.SHA256Hex,
		c.SHA384Hex,
		c.SHA512Hex,
	)
}

// StreamingChecksums writes data to an underlying writer while computing
// SHA-256, SHA-384, and SHA-512 hashes.
type StreamingChecksums struct {
	out  io.Writer
	h256 hash.Hash
	h384 hash.Hash
	h512 hash.Hash
}

// NewStreamingChecksums creates a streaming checksum writer.
func NewStreamingChecksums(out io.Writer) *StreamingChecksums {
	return &StreamingChecksums{
		out:  out,
		h256: sha256.New(),
		h384: sha512.New384(),
		h512: sha512.New(),
	}
}

// Write implements io.Writer.
func (s *StreamingChecksums) Write(p []byte) (int, error) {
	n, err := s.out.Write(p)
	if err != nil {
		return n, err
	}

	if n == 0 {
		return 0, nil
	}

	// Hash only the bytes that were successfully written.
	if _, err := s.h256.Write(p[:n]); err != nil {
		return n, err
	}
	if _, err := s.h384.Write(p[:n]); err != nil {
		return n, err
	}
	if _, err := s.h512.Write(p[:n]); err != nil {
		return n, err
	}

	return n, nil
}

// Sum returns the checksums computed so far.
func (s *StreamingChecksums) Sum() Checksums {
	return Checksums{
		SHA256Hex:       hex.EncodeToString(s.h256.Sum(nil)),
		SHA384Hex:       hex.EncodeToString(s.h384.Sum(nil)),
		SHA512Hex:       hex.EncodeToString(s.h512.Sum(nil)),
		SHA256Integrity: integrityHash("sha256", s.h256.Sum(nil)),
		SHA384Integrity: integrityHash("sha384", s.h384.Sum(nil)),
		SHA512Integrity: integrityHash("sha512", s.h512.Sum(nil)),
	}
}

// integrityHash returns an SRI-style integrity string, for example: sha256-<base64>
func integrityHash(alg string, sum []byte) string {
	return alg + "-" + base64.StdEncoding.EncodeToString(sum)
}
