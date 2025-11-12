// Package utils contains utilities for testing
package utils

import (
	"net"
)

// GetFreePort finds a free port to listen on
func GetFreePort() (string, error) {
	var listener net.Listener
	var err error
	if listener, err = net.Listen("tcp", "127.0.0.1:0"); err == nil {
		var port string
		_, port, err = net.SplitHostPort(listener.Addr().String())
		err2 := listener.Close()
		if err != nil {
			return "", err
		}
		return port, err2
	}
	return "", err
}
