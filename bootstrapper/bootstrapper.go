package bootstrapper

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/cloudfoundry/bosh-agent/bootstrapper/auth"
	"github.com/cloudfoundry/bosh-agent/bootstrapper/package_installer"
	"github.com/cloudfoundry/bosh-agent/errors"
	"github.com/cloudfoundry/bosh-agent/logger"
)

type Bootstrapper struct {
	CertFile     string
	KeyFile      string
	CACertPem    string
	AllowedNames []string

	Logger *log.Logger

	server           http.Server
	listener         net.Listener
	started          bool
	closing          bool
	wg               sync.WaitGroup
	PackageInstaller package_installer.PackageInstaller
}

const StatusUnprocessableEntity = 422

func (b *Bootstrapper) httpClient() (*http.Client, error) {
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(([]byte)(b.CACertPem)) {
		return nil, errors.Error("Failed to load CA cert")
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:    certPool,
			ClientAuth: tls.RequireAndVerifyClientCert,
		},
	}

	return &http.Client{Transport: tr}, nil
}

func (b *Bootstrapper) Download(url string) error {
	pkixNames, err := b.parseNames()
	if err != nil {
		return err
	}

	logger := logger.New(logger.LevelDebug, b.Logger, b.Logger)
	logger.Info("Download", "Downloading %s...", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logger.Error("Download", "Couldn't make the request to %s: %s", url, err.Error())
		return err
	}

	client, err := b.httpClient()
	if err != nil {
		logger.Error("Download", "Couldn't make the http client %s", err.Error())
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Download", "Couldn't do the request (%s): %s", req, err.Error())
		return err
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("Download failed, bad response: %s", resp.Status)
		logger.Error("Download", err.Error())
		return err
	}

	certificateVerifier := auth.CertificateVerifier{AllowedNames: pkixNames}

	err = certificateVerifier.Verify(resp.TLS.PeerCertificates)
	if err != nil {
		return err
	}

	logger.Info("Download", "Downloading complete. Installing...")
	err = b.PackageInstaller.Install(resp.Body)
	if err != nil {
		return err
	}

	logger.Info("Download", "Download succeeded")
	return nil
}

func (b *Bootstrapper) Listen(port int) error {
	pkixNames, err := b.parseNames()
	if err != nil {
		return err
	}

	certAuthRules := auth.CertificateVerifier{AllowedNames: pkixNames}

	serveMux := http.NewServeMux()
	logger := logger.New(logger.LevelDebug, b.Logger, b.Logger)
	serveMux.Handle("/self-update", certAuthRules.Wrap(logger, &SelfUpdateHandler{Logger: logger, packageInstaller: b.PackageInstaller}))

	b.server.Handler = serveMux
	b.server.ErrorLog = b.Logger

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil {
		return err
	}

	serverCert, err := tls.LoadX509KeyPair(b.CertFile, b.KeyFile)
	if err != nil {
		return err
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(([]byte)(b.CACertPem)) {
		return errors.Errorf("Huh? root PEM looks weird!\n%s\n", b.CACertPem)
	}
	config := &tls.Config{
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}
	b.listener = tls.NewListener(listener, config)

	b.started = true
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		err := b.server.Serve(b.listener)
		if err != nil && !b.closing {
			fmt.Printf("run(): %s\n", err)
		}
	}()

	return nil
}

func (b *Bootstrapper) StopListening() {
	if b.started {
		b.closing = true
		b.listener.Close()
		b.started = false
	}
}

func (b *Bootstrapper) WaitForServerToExit() {
	b.wg.Wait()
}

func (b *Bootstrapper) parseNames() ([]pkix.Name, error) {
	if len(b.AllowedNames) == 0 {
		return nil, errors.Error("AllowedNames must be specified")
	}

	var pkixNames []pkix.Name
	parser := auth.NewDistinguishedNamesParser()
	for _, dn := range b.AllowedNames {
		pkixName, err := parser.Parse(dn)
		if err != nil {
			return nil, errors.WrapError(err, "Invalid AllowedNames")
		}
		pkixNames = append(pkixNames, *pkixName)
	}

	return pkixNames, nil
}
