package transport

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
)

// Borrow the plaintext read-ahead buffer instead of copying each frame into a
// second scratch buffer. Peek may read only to finish the FIRST frame. Later
// frames must be complete in Buffered(), so sparse traffic never waits to batch.
func (w *httpWire) ReceiveBatch(ctx context.Context, frames [][]byte) (int, error) {
	if len(frames) == 0 || len(frames) > MaxReceiveFrames {
		return 0, errors.New("invalid receive batch limit")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if w.buffered == nil {
		w.buffered = bufio.NewReaderSize(w.reader, 64<<10)
	}
	header, err := w.buffered.Peek(2)
	if err != nil {
		return 0, err
	}
	size := int(binary.BigEndian.Uint16(header))
	if size == 0 || size > w.frameLimit() {
		return 0, errors.New("carrier frame size out of bounds")
	}
	if _, err = w.buffered.Peek(size + 2); err != nil {
		return 0, err
	}
	available, _ := w.buffered.Peek(w.buffered.Buffered())
	n, consumed := 0, 0
	for n < len(frames) && len(available)-consumed >= 2 {
		size = int(binary.BigEndian.Uint16(available[consumed:]))
		if size == 0 || size > w.frameLimit() {
			err = errors.New("carrier frame size out of bounds")
			break
		}
		end := consumed + 2 + size
		if end > len(available) {
			break
		}
		frames[n] = available[consumed+2 : end : end]
		n++
		consumed = end
	}
	// Discard only advances indices. It neither refills nor overwrites borrowed
	// bytes, which stay valid until this worker's next Receive call.
	_, _ = w.buffered.Discard(consumed)
	return n, err
}
