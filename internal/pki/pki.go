package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"github.com/DashSaman/tg/internal/config"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func Generate(dir, name string) error {
	if name == "" {
		return errors.New("server name is required")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return errors.New("PKI output directory must not exist")
	}
	serial := func(p ed25519.PublicKey) *big.Int {
		n := new(big.Int).SetBytes(p[:16])
		if n.Sign() == 0 {
			n.SetInt64(1)
		}
		return n
	}
	now := time.Now()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	ca := &x509.Certificate{SerialNumber: serial(pub), Subject: pkix.Name{CommonName: "TG private test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(5, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		return err
	}
	write := func(name string, b []byte) error {
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	keyPEM := func(k ed25519.PrivateKey) ([]byte, error) {
		b, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			return nil, e
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), nil
	}
	caKey, err := keyPEM(key)
	if err != nil {
		return err
	}
	if err := write("ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return err
	}
	if err := write("ca.key", caKey); err != nil {
		return err
	}
	for _, role := range []string{"server", "edge"} {
		p, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		cert := &x509.Certificate{SerialNumber: serial(p), NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}
		if role == "server" {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			if ip := net.ParseIP(name); ip != nil {
				cert.IPAddresses = []net.IP{ip}
			} else {
				cert.DNSNames = []string{name}
			}
		} else {
			cert.DNSNames = []string{"tg-edge"}
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		b, e := x509.CreateCertificate(rand.Reader, cert, ca, p, key)
		if e != nil {
			return e
		}
		kb, e := keyPEM(k)
		if e != nil {
			return e
		}
		if e = write(role+".crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})); e != nil {
			return e
		}
		if e = write(role+".key", kb); e != nil {
			return e
		}
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	return write("web.token", []byte(hex.EncodeToString(token)+"\n"))
}

// Check verifies the local leaf's role, name, expiry and trust chain before binding.
func Check(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	pool, err := roots(c.TLS.CA)
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(c.TLS.Cert, c.TLS.Key)
	if err != nil {
		return errors.New("cannot load TLS identity")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("invalid TLS leaf")
	}
	usage := x509.ExtKeyUsageClientAuth
	name := c.TLS.PeerName
	if c.Role == "exit" {
		usage = x509.ExtKeyUsageServerAuth
		name = c.TLS.ServerName
	}
	intermediate := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, e := x509.ParseCertificate(der)
		if e != nil {
			return errors.New("invalid TLS chain")
		}
		intermediate.AddCert(cert)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: intermediate, DNSName: name, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return errors.New("TLS identity is expired, untrusted or has wrong role/name")
	}
	for _, p := range c.Carriers {
		if p.Kind == "https" {
			_, err = config.ReadToken(c.TokenFile)
			return err
		}
	}
	return nil
}

func roots(path string) (*x509.CertPool, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("cannot read TLS CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("invalid TLS CA")
	}
	return pool, nil
}

func Client(c config.TLS, mutual bool) (*tls.Config, error) {
	pool, e := roots(c.CA)
	if e != nil {
		return nil, e
	}
	out := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: c.ServerName}
	if mutual {
		pair, e := tls.LoadX509KeyPair(c.Cert, c.Key)
		if e != nil {
			return nil, errors.New("cannot load TLS client identity")
		}
		out.Certificates = []tls.Certificate{pair}
	}
	return out, nil
}

func Server(c config.TLS, mutual bool) (*tls.Config, error) {
	pair, e := tls.LoadX509KeyPair(c.Cert, c.Key)
	if e != nil {
		return nil, errors.New("cannot load TLS server identity")
	}
	out := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	if mutual {
		pool, e := roots(c.CA)
		if e != nil {
			return nil, e
		}
		out.ClientCAs = pool
		out.ClientAuth = tls.RequireAndVerifyClientCert
		out.VerifyConnection = func(s tls.ConnectionState) error {
			if len(s.PeerCertificates) == 0 {
				return errors.New("missing peer identity")
			}
			return s.PeerCertificates[0].VerifyHostname(c.PeerName)
		}
	}
	return out, nil
}
