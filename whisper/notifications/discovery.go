package notifications

import (
	"errors"
	"fmt"

	"encoding/hex"
	"encoding/json"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/logger"
	"github.com/ethereum/go-ethereum/logger/glog"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv5"
)

const (
	protocolKeyName            = "NOTIFICATION_PROTOCOL_KEY"
	topicDiscoverServer        = "DISCOVER_NOTIFICATION_SERVER"
	topicProposeServer         = "PROPOSE_NOTIFICATION_SERVER"
	topicServerAccepted        = "ACCEPT_NOTIFICATION_SERVER"
	topicAckClientSubscription = "ACK_NOTIFICATION_SERVER_SUBSCRIPTION"
)

// discoveryService abstract notification server discovery protocol
type discoveryService struct {
	server *NotificationServer

	discoverFilterID       string
	serverAcceptedFilterID string
}

// messageProcessingFn is a callback used to process incoming client requests
type messageProcessingFn func(*whisper.ReceivedMessage) error

func NewDiscoveryService(notificationServer *NotificationServer) *discoveryService {
	return &discoveryService{
		server: notificationServer,
	}
}

// Start installs necessary filters to watch for incoming discovery requests,
// then in separate routine starts watcher loop
func (s *discoveryService) Start() error {
	var err error

	// notification server discovery requests
	s.discoverFilterID, err = s.server.installTopicFilter(topicDiscoverServer, s.server.protocolKey)
	if err != nil {
		return fmt.Errorf("failed installing filter: %v", err)
	}
	go s.server.requestProcessorLoop(s.discoverFilterID, topicDiscoverServer, s.processDiscoveryRequest)

	// notification server accept/select requests
	s.serverAcceptedFilterID, err = s.server.installTopicFilter(topicServerAccepted, s.server.protocolKey)
	if err != nil {
		return fmt.Errorf("failed installing filter: %v", err)
	}
	go s.server.requestProcessorLoop(s.serverAcceptedFilterID, topicServerAccepted, s.processServerAcceptedRequest)

	glog.V(logger.Info).Infoln("notification server discovery service started")
	return nil
}

// Stop stops all discovery processing loops
func (s *discoveryService) Stop() error {
	s.server.whisper.Unwatch(s.discoverFilterID)
	s.server.whisper.Unwatch(s.serverAcceptedFilterID)

	glog.V(logger.Info).Infoln("notification server discovery service stopped")
	return nil
}

// processDiscoveryRequest processes incoming client requests of type:
// when client tries to discover suitable notification server
func (s *discoveryService) processDiscoveryRequest(msg *whisper.ReceivedMessage) error {
	// offer this node as notification server
	msgParams := whisper.MessageParams{
		Dst:      msg.Src,
		KeySym:   s.server.protocolKey,
		Topic:    MakeTopic([]byte(topicProposeServer)),
		Payload:  []byte(`{"server": "0x` + s.server.nodeID + `"}`),
		TTL:      uint32(s.server.config.TTL),
		PoW:      s.server.config.MinimumPoW,
		WorkTime: 5,
	}
	response := whisper.NewSentMessage(&msgParams)
	env, err := response.Wrap(&msgParams)
	if err != nil {
		return fmt.Errorf("failed to wrap server proposal message: %v", err)
	}

	if err := s.server.whisper.Send(env); err != nil {
		return fmt.Errorf("failed to send server proposal message: %v", err)
	}

	glog.V(logger.Debug).Infof("server proposal message sent (dst: %v, topic: %x)", msgParams.Dst, msgParams.Topic)
	return nil
}

// processServerAcceptedRequest processes incoming client requests of type:
// when client is ready to select the given node as its notification server
func (s *discoveryService) processServerAcceptedRequest(msg *whisper.ReceivedMessage) error {
	var parsedMessage struct {
		ServerID string `json:"server"`
	}
	if err := json.Unmarshal(msg.Payload, &parsedMessage); err != nil {
		return err
	}

	if msg.Src == nil {
		return errors.New("message 'from' field is required")
	}

	// make sure that only requests made to the current node are processed
	if parsedMessage.ServerID != `0x`+s.server.nodeID {
		return nil
	}

	// register client
	sessionKey, err := s.server.RegisterClientSession(&ClientSession{
		ClientKey: hex.EncodeToString(crypto.FromECDSAPub(msg.Src)),
	})
	if err != nil {
		return err
	}

	// confirm that client has been successfully subscribed
	msgParams := whisper.MessageParams{
		Dst:      msg.Src,
		KeySym:   s.server.protocolKey,
		Topic:    MakeTopic([]byte(topicAckClientSubscription)),
		Payload:  []byte(`{"server": "0x` + s.server.nodeID + `", "key": "0x` + hex.EncodeToString(sessionKey) + `"}`),
		TTL:      uint32(s.server.config.TTL),
		PoW:      s.server.config.MinimumPoW,
		WorkTime: 5,
	}
	response := whisper.NewSentMessage(&msgParams)
	env, err := response.Wrap(&msgParams)
	if err != nil {
		return fmt.Errorf("failed to wrap server proposal message: %v", err)
	}

	if err := s.server.whisper.Send(env); err != nil {
		return fmt.Errorf("failed to send server proposal message: %v", err)
	}

	glog.V(logger.Debug).Infof("server confirms client subscription (dst: %v, topic: %x)", msgParams.Dst, msgParams.Topic)
	return nil
}
