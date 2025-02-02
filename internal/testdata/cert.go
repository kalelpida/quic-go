package testdata

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"path"
)

var CertPath string

// GetCertificatePaths returns the paths to certificate and key
func GetCertificatePaths() (string, string) {
	return path.Join(CertPath, "cert.pem"), path.Join(CertPath, "priv.key")
}

// GetTLSConfig returns a tls config for quic.clemente.io
func GetTLSConfig() *tls.Config {
	cert, err := tls.LoadX509KeyPair(GetCertificatePaths())
	if err != nil {
		panic(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
}

// AddRootCA adds the root CA certificate to a cert pool
func AddRootCA(certPool *x509.CertPool) {
	caCertPath := path.Join(CertPath, "ca.pem")
	caCertRaw, err := ioutil.ReadFile(caCertPath)
	if err != nil {
		panic(err)
	}
	if ok := certPool.AppendCertsFromPEM(caCertRaw); !ok {
		panic("Could not add root ceritificate to pool.")
	}
}

// GetRootCA returns an x509.CertPool containing (only) the CA certificate
func GetRootCA() *x509.CertPool {
	pool := x509.NewCertPool()
	AddRootCA(pool)
	return pool
}
