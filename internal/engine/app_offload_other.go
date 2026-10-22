//go:build !linux

package engine

import "net"

func (a *application) prepareOffloadedWrite(_ [][]byte, _ net.Addr) int { return 0 }
func unsupportedApplicationGSO(error) bool                              { return false }
