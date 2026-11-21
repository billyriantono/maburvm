// Command fake-agent serves a stand-in node agent for browser tests and local
// development: a panel pointed at it creates, powers and backs up VMs that
// exist only in this process's memory. It is never built into a release or an
// image.
//
// The panel dials every node on port 50051 over TLS and trusts the first
// certificate it sees, so add a node with address 127.0.0.1 and run:
//
//	go run ./cmd/fake-agent -token <the node's token>
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"log"
	"math/big"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/maburvm/panel/internal/shared/fakeagent"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:50051", "address to serve the agent API on")
	token := flag.String("token", "", "bearer token calls must carry (empty accepts any)")
	vms := flag.String("vms", "", "VMs the node already holds, as id=running|stopped, comma separated")
	flag.Parse()

	cert, err := selfSigned()
	if err != nil {
		log.Fatalf("certificate: %v", err)
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	agent := fakeagent.New()
	agent.Token = *token
	for _, v := range strings.Split(*vms, ",") {
		if id, state, _ := strings.Cut(strings.TrimSpace(v), "="); id != "" {
			s := pb.VMState_VM_STATE_RUNNING
			if state == "stopped" {
				s = pb.VMState_VM_STATE_STOPPED
			}
			agent.PutVM(fakeagent.VM{ID: id, State: s, Config: &pb.VMConfig{}})
		}
	}
	opts := append(agent.ServerOptions(), grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	srv := grpc.NewServer(opts...)
	pb.RegisterNodeAgentServer(srv, agent)
	log.Printf("fake agent listening on %s", lis.Addr())
	log.Fatal(srv.Serve(lis))
}

func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "fake-agent"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
