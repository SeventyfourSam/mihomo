// Local integration-test fixture only; never imported or linked into mihomo.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/server"
)

func main() {
	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		panic(err)
	}
	c := &v1.ServerConfig{BindAddr: "127.0.0.1", BindPort: port, ProxyBindAddr: "127.0.0.1",
		Auth: v1.AuthServerConfig{Method: v1.AuthMethodToken, Token: "integration-token"}}
	c.Transport.TLS.Force = true
	c.Complete()
	s, err := server.NewService(c)
	if err != nil {
		panic(err)
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go s.Run(context.Background())
	fmt.Println("FRPS_READY")
	<-done
	s.Close()
}
