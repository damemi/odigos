package bootstrap

import (
	"os"
	"os/signal"
	"syscall"
)

func GetshutdownChannel() <-chan struct{} {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	shutdownCh := make(chan struct{})
	go func() {
		<-sigChan
		close(shutdownCh)
	}()
	return shutdownCh
}
