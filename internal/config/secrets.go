package config

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

type Material struct {
	CACertificate     string `json:"ca_certificate,omitempty"`
	CAKey             string `json:"ca_key,omitempty"`
	Certificate       string `json:"certificate,omitempty"`
	Key               string `json:"key,omitempty"`
	ClientCertificate string `json:"client_certificate,omitempty"`
	ClientKey         string `json:"client_key,omitempty"`
	TLSCrypt          string `json:"tls_crypt,omitempty"`
	OTPKey            string `json:"otp_key,omitempty"`
}

const (
	MaterialAll      = "all"
	MaterialPKI      = "pki"
	MaterialServer   = "server"
	MaterialClient   = "client"
	MaterialTLSCrypt = "tls_crypt"
	MaterialOTP      = "otp_key"
	MaterialTLS      = "tls"
)

const certificateLifetime = 10 * 365 * 24 * time.Hour
const certificateBackdate = time.Hour
const staticKeySize = 256
const staticKeyLine = 16
const otpKeySize = 20
const authorityName = "Proximity CA"
const serverName = "proximity-server"
const clientName = "proximity-client"
const certificateBlock = "CERTIFICATE"
const staticKeyHeader = "-----BEGIN OpenVPN Static key V1-----"
const staticKeyFooter = "-----END OpenVPN Static key V1-----"

var staticKeyPattern = regexp.MustCompile(`^` + staticKeyHeader + `\n([0-9a-fA-F]{32}\n){16}` +
	staticKeyFooter + `\n$`)

func GenerateMaterial(kind string, source Material, now time.Time) (Material, error) {
	switch kind {
	case MaterialAll:
		result, err := generateChain(now)
		if err != nil {
			return Material{}, err
		} else if result.TLSCrypt, err = generateStaticKey(); err != nil {
			return Material{}, err
		} else if result.OTPKey, err = generateOTPKey(); err != nil {
			return Material{}, err
		}
		return result, nil
	case MaterialPKI:
		return generateChain(now)
	case MaterialServer:
		certificate, key, err := issueLeaf(source, serverName, x509.ExtKeyUsageServerAuth, now)
		return Material{Certificate: certificate, Key: key}, err
	case MaterialClient:
		certificate, key, err := issueLeaf(source, clientName, x509.ExtKeyUsageClientAuth, now)
		return Material{ClientCertificate: certificate, ClientKey: key}, err
	case MaterialTLSCrypt:
		key, err := generateStaticKey()
		return Material{TLSCrypt: key}, err
	case MaterialOTP:
		key, err := generateOTPKey()
		return Material{OTPKey: key}, err
	case MaterialTLS:
		template, err := makeTemplate(serverName, now)
		if err != nil {
			return Material{}, err
		}
		template.KeyUsage, template.DNSNames = x509.KeyUsageDigitalSignature, []string{serverName}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		certificate, key, err := issue(template, nil, nil)
		return Material{Certificate: certificate, Key: key}, err
	default:
		return Material{}, fmt.Errorf("unexpected material kind '%v'", kind)
	}
}

func CommonName(certificate string) string {
	if parsed, err := parseCertificate(certificate); err == nil {
		return parsed.Subject.CommonName
	}
	return ""
}

func normalizePEM(text string) string {
	if text = strings.TrimSpace(strings.ReplaceAll(text, "\r", "")); text == "" {
		return ""
	}
	return text + "\n"
}

func generateChain(now time.Time) (Material, error) {
	template, err := makeTemplate(authorityName, now)
	if err != nil {
		return Material{}, err
	}
	template.IsCA, template.KeyUsage = true, x509.KeyUsageCertSign|x509.KeyUsageCRLSign
	result := Material{}
	if result.CACertificate, result.CAKey, err = issue(template, nil, nil); err != nil {
		return Material{}, err
	} else if result.Certificate, result.Key, err = issueLeaf(result, serverName,
		x509.ExtKeyUsageServerAuth, now); err != nil {
		return Material{}, err
	} else if result.ClientCertificate, result.ClientKey, err = issueLeaf(result, clientName,
		x509.ExtKeyUsageClientAuth, now); err != nil {
		return Material{}, err
	}
	return result, nil
}

func generateStaticKey() (string, error) {
	buffer := make([]byte, staticKeySize)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("failed to read random data: %w", err)
	}
	result := strings.Builder{}
	result.WriteString(staticKeyHeader + "\n")
	for offset := 0; offset < len(buffer); offset += staticKeyLine {
		result.WriteString(hex.EncodeToString(buffer[offset:offset+staticKeyLine]) + "\n")
	}
	result.WriteString(staticKeyFooter + "\n")
	return result.String(), nil
}

func generateOTPKey() (string, error) {
	buffer := make([]byte, otpKeySize)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("failed to read random data: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), nil
}

func makeTemplate(name string, now time.Time) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, fmt.Errorf("failed to read random data: %w", err)
	}
	return &x509.Certificate{SerialNumber: serial.Add(serial, big.NewInt(1)),
		Subject:   pkix.Name{CommonName: name},
		NotBefore: now.Add(-certificateBackdate), NotAfter: now.Add(certificateLifetime),
		BasicConstraintsValid: true}, nil
}

func issueLeaf(source Material, name string, usage x509.ExtKeyUsage,
	now time.Time) (string, string, error) {
	authority, err := parseCertificate(source.CACertificate)
	if err != nil {
		return "", "", fmt.Errorf("failed to check CA certificate: %w", err)
	}
	signer, err := parseKey(source.CAKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to check CA private key: %w", err)
	} else if err = checkPair(source.CACertificate, source.CAKey); err != nil {
		return "", "", fmt.Errorf("failed to check CA: %w", err)
	}
	template, err := makeTemplate(name, now)
	if err != nil {
		return "", "", err
	}
	template.KeyUsage, template.ExtKeyUsage = x509.KeyUsageDigitalSignature,
		[]x509.ExtKeyUsage{usage}
	return issue(template, authority, signer)
}

func issue(template *x509.Certificate, parent *x509.Certificate,
	signer crypto.Signer) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}
	if parent == nil {
		parent, signer = template, key
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey,
		signer)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate '%v': %w",
			template.Subject.CommonName, err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("failed to encode private key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: certificateBlock, Bytes: certificate})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})), nil
}

func parseCertificate(text string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil || block.Type != certificateBlock {
		return nil, errors.New("PEM certificate does not exist")
	}
	result, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	return result, nil
}

func parseKey(text string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("PEM private key does not exist")
	} else if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, found := key.(crypto.Signer); found {
			return signer, nil
		}
		return nil, errors.New("unexpected private key type")
	} else if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	} else if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unexpected private key format")
}

func checkPair(certificate string, key string) error {
	if _, err := tls.X509KeyPair([]byte(certificate), []byte(key)); err != nil {
		return fmt.Errorf("failed to match certificate and private key: %w", err)
	}
	return nil
}

func checkIssued(certificate string, authority string) error {
	parent, err := parseCertificate(authority)
	if err != nil {
		return fmt.Errorf("failed to check CA certificate: %w", err)
	}
	child, err := parseCertificate(certificate)
	if err != nil {
		return err
	} else if err = child.CheckSignatureFrom(parent); err != nil {
		return fmt.Errorf("failed to check signature of '%v': %w", child.Subject.CommonName, err)
	}
	return nil
}

func checkStaticKey(text string) error {
	if !staticKeyPattern.MatchString(text) {
		return errors.New("invalid OpenVPN static key")
	}
	return nil
}
