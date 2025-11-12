package utils

import (
	"net"
)

func GetFreePort() (string, error) {
	if listener, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		_, port, err := net.SplitHostPort(listener.Addr().String())
		err2 := listener.Close()
		if err != nil {
			return "", err
		}
		return port, err2
	} else {
		return "", err
	}
}
