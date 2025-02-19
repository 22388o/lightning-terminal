package session

import (
	"crypto/tls"
	"fmt"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type sessionID [33]byte

type GRPCServerCreator func(opts ...grpc.ServerOption) *grpc.Server

type mailboxSession struct {
	server *grpc.Server

	wg sync.WaitGroup
}

func (m *mailboxSession) start(session *Session,
	serverCreator GRPCServerCreator, authData []byte) error {

	tlsConfig := &tls.Config{}
	if session.DevServer {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// Start the mailbox gRPC server.
	mailboxServer, err := mailbox.NewServer(
		session.ServerAddr, session.PairingSecret[:],
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)
	if err != nil {
		return err
	}

	ecdh := &keychain.PrivKeyECDH{PrivKey: session.LocalPrivateKey}
	noiseConn := mailbox.NewNoiseGrpcConn(
		ecdh, authData, session.PairingSecret[:],
	)
	m.server = serverCreator(grpc.Creds(noiseConn))

	m.wg.Add(1)
	go m.run(mailboxServer)

	return nil
}

func (m *mailboxSession) run(mailboxServer *mailbox.Server) {
	defer m.wg.Done()

	log.Infof("Mailbox RPC server listening on %s", mailboxServer.Addr())
	if err := m.server.Serve(mailboxServer); err != nil {
		log.Errorf("Unable to serve mailbox gRPC: %v", err)
	}
}

func (m *mailboxSession) stop() {
	m.server.Stop()
	m.wg.Wait()
}

type Server struct {
	serverCreator GRPCServerCreator

	activeSessions    map[sessionID]*mailboxSession
	activeSessionsMtx sync.Mutex

	quit chan struct{}
}

func NewServer(serverCreator GRPCServerCreator) *Server {
	return &Server{
		serverCreator:  serverCreator,
		activeSessions: make(map[sessionID]*mailboxSession),
		quit:           make(chan struct{}),
	}
}

func (s *Server) StartSession(session *Session, authData []byte) error {
	s.activeSessionsMtx.Lock()
	defer s.activeSessionsMtx.Unlock()

	var id sessionID
	copy(id[:], session.LocalPublicKey.SerializeCompressed())

	_, ok := s.activeSessions[id]
	if ok {
		return fmt.Errorf("session %x is already active", id[:])
	}

	s.activeSessions[id] = &mailboxSession{}
	return s.activeSessions[id].start(session, s.serverCreator, authData)
}

func (s *Server) StopSession(localPublicKey *btcec.PublicKey) error {
	s.activeSessionsMtx.Lock()
	defer s.activeSessionsMtx.Unlock()

	var id sessionID
	copy(id[:], localPublicKey.SerializeCompressed())

	_, ok := s.activeSessions[id]
	if !ok {
		return fmt.Errorf("session %x is not active", id[:])
	}

	s.activeSessions[id].stop()
	delete(s.activeSessions, id)

	return nil
}

func (s *Server) Stop() {
	s.activeSessionsMtx.Lock()
	defer s.activeSessionsMtx.Unlock()

	for id, session := range s.activeSessions {
		session.stop()
		delete(s.activeSessions, id)
	}
}
