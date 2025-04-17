package consts

import "errors"

type ProxyConnectionStatus string

const (
	StatusConnected    ProxyConnectionStatus = "success"
	StatusDisconnected ProxyConnectionStatus = "error"
)

func (w ProxyConnectionStatus) MarshalBinary() ([]byte, error) {
	return []byte(w), nil
}

func (w *ProxyConnectionStatus) UnmarshalBinary(data []byte) error {
	str := string(data)
	switch ProxyConnectionStatus(str) {
	case StatusConnected, StatusDisconnected:
		*w = ProxyConnectionStatus(str)
		return nil
	default:
		return errors.New("invalid ProxyConnectionStatus")
	}
}
