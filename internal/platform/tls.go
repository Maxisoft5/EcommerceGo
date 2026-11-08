package platform

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func MutualTLS(server bool) (*tls.Config, error) {
	ca, e := os.ReadFile(MustEnv("GRPC_CA"))
	if e != nil {
		return nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("invalid CA")
	}
	cert, e := tls.LoadX509KeyPair(MustEnv("GRPC_CERT"), MustEnv("GRPC_KEY"))
	if e != nil {
		return nil, e
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: pool}
	if server {
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		cfg.ServerName = "inventory"
	}
	return cfg, nil
}
