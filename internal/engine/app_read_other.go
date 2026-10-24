//go:build !linux

package engine

import "net/netip"

func (a *application) setupReadBatch(int, bool) {}
func (a *application) readBatchPacket(int) ([]byte, netip.AddrPort, error) {
	panic("Linux-only batch reader")
}
