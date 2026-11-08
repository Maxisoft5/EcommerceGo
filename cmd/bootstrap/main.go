// bootstrap creates local development credentials. It never changes the OS trust store.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func random() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func write(path string, b []byte) {
	if e := os.WriteFile(path, b, 0600); e != nil {
		panic(e)
	}
}
func pemWrite(name, kind string, b []byte) {
	// The parent directory is 0700 on Unix. Individual mounts must be readable by UID 10001.
	if e := os.WriteFile(filepath.Join(".secrets", name), pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b}), 0644); e != nil {
		panic(e)
	}
}
func main() {
	if _, e := os.Stat(".env"); e == nil {
		fmt.Println("Already initialized: .env exists. Credentials preserved.")
		return
	}
	if e := os.MkdirAll(".secrets", 0700); e != nil {
		panic(e)
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		panic(e)
	}
	b, e := x509.MarshalPKCS8PrivateKey(priv)
	if e != nil {
		panic(e)
	}
	pemWrite("jwt-private.pem", "PRIVATE KEY", b)
	b, e = x509.MarshalPKIXPublicKey(pub)
	if e != nil {
		panic(e)
	}
	pemWrite("jwt-public.pem", "PUBLIC KEY", b)
	caKey, e := rsa.GenerateKey(rand.Reader, 3072)
	if e != nil {
		panic(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "FlashCart Local Development CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if e != nil {
		panic(e)
	}
	pemWrite("ca.pem", "CERTIFICATE", der)
	for i, name := range []string{"gateway", "inventory", "orders"} {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			panic(e)
		}
		uri, _ := url.Parse("spiffe://flashcart/" + name)
		cert := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), DNSNames: []string{name, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, e = x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if e != nil {
			panic(e)
		}
		pemWrite(name+".pem", "CERTIFICATE", der)
		pemWrite(name+"-key.pem", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	}
	env := map[string]string{"POSTGRES_PASSWORD": random(), "IDENTITY_DB_PASSWORD": random(), "CATALOG_DB_PASSWORD": random(), "INVENTORY_DB_PASSWORD": random(), "ORDERS_DB_PASSWORD": random(), "REDIS_PASSWORD": random(), "CLICKHOUSE_PASSWORD": random(), "ADMIN_EMAIL": "admin@flashcart.local", "ADMIN_PASSWORD": "Admin-" + random(), "PUBLIC_ORIGIN": "https://localhost:8443"}
	for _, v := range []struct{ prefix, user, host, db string }{{"IDENTITY", "identity", "postgres", "identity"}, {"CATALOG", "catalog", "postgres", "catalog"}, {"INVENTORY", "inventory", "postgres", "inventory"}, {"ORDERS_SHARD_0", "orders", "orders-db-0", "orders"}, {"ORDERS_SHARD_1", "orders", "orders-db-1", "orders"}} {
		passwordKey := v.prefix + "_DB_PASSWORD"
		if strings.HasPrefix(v.prefix, "ORDERS_") {
			passwordKey = "ORDERS_DB_PASSWORD"
		}
		env[v.prefix+"_URL"] = "postgres://" + v.user + ":" + env[passwordKey] + "@" + v.host + ":5432/" + v.db + "?sslmode=disable"
	}
	env["REDIS_URL"] = "redis://:" + env["REDIS_PASSWORD"] + "@redis:6379/0"
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var content strings.Builder
	content.WriteString("# Generated local development credentials. Never commit this file.\n")
	for _, k := range keys {
		fmt.Fprintf(&content, "%s=%s\n", k, env[k])
	}
	write(".env", []byte(content.String()))
	fmt.Println("Created .env and .secrets/. Start: docker compose up --build -d\nOpen https://localhost:8443 (CA: .secrets/ca.pem). Admin credentials are in .env.")
}
