// Package wire implements the monty.v1 protocol spoken between a parent and a
// Monty worker: 4-byte little-endian length framing and a hand-written
// protobuf codec that charges a resident-memory budget while decoding.
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxFrameLen is the largest frame either side accepts.
	MaxFrameLen = 256 << 20
	// DefaultMaxDecodeBytes bounds the host memory one decoded frame may occupy.
	DefaultMaxDecodeBytes = 4 * MaxFrameLen
)

var (
	// ErrTruncated reports a stream that ended inside a frame.
	ErrTruncated = errors.New("frame stream truncated")
	// ErrDecodeBudget reports a frame whose decoded values exceed the budget.
	ErrDecodeBudget = errors.New("decoded frame exceeds the host memory budget")
)

// FrameTooLargeError reports a frame longer than MaxFrameLen.
type FrameTooLargeError struct {
	Len int
	Max int
}

func (e *FrameTooLargeError) Error() string {
	return fmt.Sprintf("frame of %d bytes exceeds maximum of %d bytes", e.Len, e.Max)
}

// FrameReader reads length-prefixed frames.
type FrameReader struct {
	r *bufio.Reader
}

// NewFrameReader wraps r.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next frame, io.EOF at a clean frame boundary, or ErrTruncated.
func (f *FrameReader) Next() ([]byte, error) {
	var hdr [4]byte
	n, err := io.ReadFull(f.r, hdr[:])
	if err != nil {
		if n == 0 && errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, ErrTruncated
		}
		return nil, err
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	if size > MaxFrameLen {
		return nil, &FrameTooLargeError{Len: int(size), Max: MaxFrameLen}
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(f.r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, ErrTruncated
		}
		return nil, err
	}
	return buf, nil
}

// AppendFrameHeader prefixes payload with its length.
func AppendFrameHeader(payload []byte) ([]byte, error) {
	if len(payload) > MaxFrameLen {
		return nil, &FrameTooLargeError{Len: len(payload), Max: MaxFrameLen}
	}
	out := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(out, uint32(len(payload)))
	copy(out[4:], payload)
	return out, nil
}
