package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"
)

const accountSocket = "/run/htool-account.sock"

func activatedListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
		return nil, errors.New("expected one systemd socket")
	}
	file := os.NewFile(3, "htool-systemd")
	defer file.Close()
	return net.FileListener(file)
}

func certificate(path string) (tls.Certificate, error) {
	if data, err := os.ReadFile(path); err == nil {
		return tls.X509KeyPair(data, data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Houzzkit Force1"},
		IPAddresses:  []net.IP{net.ParseIP("192.168.55.1")},
		NotBefore:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2050, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	data := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})...)
	if err := atomicWrite(path, data); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(data, data)
}

func runWeb(listener net.Listener) error {
	cert, err := certificate("/var/lib/htool-web/device.pem")
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           webHandler{request: requestAccount},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}},
	}
	return server.ServeTLS(listener, "", "")
}

func runAccounts(listener net.Listener) error {
	webUser, err := user.Lookup("htool")
	if err != nil {
		return err
	}
	uid, err := strconv.ParseUint(webUser.Uid, 10, 32)
	if err != nil || uid == 0 {
		return errors.New("invalid htool account UID")
	}
	service := &setup{
		accounts:  systemAccounts{},
		statePath: "/var/lib/htool-account/setup.json",
		guardPath: "/etc/htool/initialized.json",
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			if !isPeer(conn, uint32(uid)) {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			var message accountRequest
			if decodeJSON(conn, &message) != nil {
				return
			}
			var reply result
			if message.Setup == nil {
				reply = service.status()
			} else {
				reply = service.apply(*message.Setup)
			}
			_ = json.NewEncoder(conn).Encode(reply)
		}()
	}
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: htool web|account")
	}
	listener, err := activatedListener()
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	switch os.Args[1] {
	case "web":
		err = runWeb(listener)
	case "account":
		err = runAccounts(listener)
	default:
		err = errors.New("usage: htool web|account")
	}
	log.Fatal(err)
}
