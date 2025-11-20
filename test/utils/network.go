// Package utils contains utilities for testing
//
//revive:disable:var-naming
package utils

//revive:enable:var-naming

import (
	"net"
)

// GetFreePort finds a free port to listen on
func GetFreePort() (string, error) {
	var listener net.Listener
	var err error
	if listener, err = net.Listen("tcp", ":0"); err == nil {
		var port string
		_, port, err = net.SplitHostPort(listener.Addr().String())
		defer func() {
			_ = listener.Close()
		}()
		return port, err
	}
	return "", err
}
