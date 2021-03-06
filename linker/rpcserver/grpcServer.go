package rpcserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/Axway/ace-golang-sdk/rpc"
	"github.com/Axway/ace-golang-sdk/util"
	"github.com/Axway/ace-golang-sdk/util/logging"
	"github.com/Axway/ace-golang-sdk/util/tracing"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

var log = logging.Logger()

// OnRelayHandler defines what will be called in Relay method when payload is received; different implementation
// is used by Linker and by Sidecar
type OnRelayHandler func(msg *rpc.Message)

// OnRelayCompleteHandler -
type OnRelayCompleteHandler func(msg *rpc.Message)

// OnRelayCompleteErrorHandler -
type OnRelayCompleteErrorHandler func(*rpc.Message, error)

// OnRegistrationCompleteHandler handles registration info in application specific way
type OnRegistrationCompleteHandler func(serviceInfo *rpc.ServiceInfo) error

// Server implements LinkageService interface methods
type Server struct {
	OnRelay                OnRelayHandler
	OnRelayComplete        OnRelayCompleteHandler
	OnRelayCompleteError   OnRelayCompleteErrorHandler
	OnRegistrationComplete OnRegistrationCompleteHandler
	Name                   string
}

// Registration implements LinkageService interface
func (s *Server) Registration(ctx context.Context, serviceInfo *rpc.ServiceInfo) (*rpc.Receipt, error) {
	err := s.OnRegistrationComplete(serviceInfo)

	if err != nil {
		log.Error("Error in service registration", zap.String(logging.LogFieldError, err.Error()))
		return &rpc.Receipt{
			IsOk:  false,
			Error: err.Error(),
		}, err
	}
	return &rpc.Receipt{IsOk: true}, nil
}

func (s *Server) processOnRelayComplete(last *rpc.Message, msgCount uint64) {
	//done with Recv, so if recErr == nil, sidecar will need to commit
	log.Debug("Message relay complete",
		zap.String(logging.LogFieldServiceName, s.Name),
		zap.Uint64(logging.LogFieldMessageCount, msgCount),
	)
	if last != nil {
		last.SequenceUpperBound = msgCount
		if msgCount > 1 {
			if last.GetMetaData() == nil {
				last.MetaData = make(map[string]string)
			}
			// If more than one message has been sent from the service set the SequenceUUID to the ParentUUID
			last.MetaData["SequenceUUID"] = last.GetParent_UUID()
			last.MetaData["SequenceTerm"] = strconv.Itoa(int(msgCount))
			last.MetaData["SequenceUpperBound"] = strconv.Itoa(int(msgCount))
		}
		s.OnRelayComplete(last)
	} else {
		log.Fatal("Unexpected end of message stream, expected to have at least one message",
			zap.String(logging.LogFieldServiceName, s.Name))
	}
}
func (s *Server) sendReceipt(stream rpc.Linkage_RelayServer, isOK bool) {
	var receipt = &rpc.Receipt{IsOk: isOK}
	if err := stream.Send(receipt); err != nil {
		log.Error("Error in sending relay receipt",
			zap.String(logging.LogFieldServiceName, s.Name),
			zap.String(logging.LogFieldError, err.Error()),
		)
	}
	log.Debug("Relay receipt sent",
		zap.String(logging.LogFieldServiceName, s.Name),
	)
}

func (s *Server) processOnRelay(msg *rpc.Message, last *rpc.Message, msgCount uint64) {
	if last != nil {
		if msgCount > 1 {
			if last.GetMetaData() == nil {
				last.MetaData = make(map[string]string)
			}
			// If more than one message has been sent from the service set the SequenceUUID to the ParentUUID
			last.MetaData["SequenceUUID"] = last.GetParent_UUID()
			last.MetaData["SequenceTerm"] = strconv.Itoa(int(last.GetSequenceTerm()))
		}
		s.OnRelay(last)
	}
}

// Relay implements LinkageService interface
func (s *Server) Relay(stream rpc.Linkage_RelayServer) error {
	// receive data from stream
	log.Debug("Starting to receive relay messages on stream",
		zap.String(logging.LogFieldServiceName, s.Name),
	)

	var msgCount uint64
	var last *rpc.Message
	for {
		in, recErr := stream.Recv()

		if recErr == io.EOF {
			s.processOnRelayComplete(last, msgCount)
			return nil
		} else if recErr != nil {
			log.Error("Error in receving relay messages",
				zap.String(logging.LogFieldError, recErr.Error()),
			)
			s.OnRelayCompleteError(last, recErr)
			return recErr
		}
		log.Debug("Received message to relay",
			zap.String(logging.LogFieldServiceName, s.Name),
			zap.String(logging.LogFieldParentMessageID, in.Parent_UUID),
			zap.String(logging.LogFieldMessageID, in.UUID))

		isOK := (recErr == nil)
		s.sendReceipt(stream, isOK)

		if isOK {
			msg := in
			msgCount++
			msg.SequenceTerm = msgCount
			s.processOnRelay(msg, last, msgCount)
			last = msg
		}
	}
}

// StartServer creates new grpc LinkageService server returns error to prevent registration to take place
func StartServer(host string, port uint16, server *Server) {
	gracefulStop := util.CreateSignalChannel()

	log.Debug("Starting server",
		zap.String(logging.LogFieldServiceName, server.Name),
		zap.String(logging.LogFieldServiceHost, host),
		zap.Uint16(logging.LogFieldServicePort, port),
	)

	// create listener
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		log.Fatal("Server failed to listen",
			zap.String(logging.LogFieldError, err.Error()),
		)
	}

	options := []grpc.ServerOption{
		grpc.UnaryInterceptor(tracing.GetOpenTracingServerInterceptor()),
	}
	// create grpc server
	grpcServer := grpc.NewServer(options...)

	rpc.RegisterLinkageServer(grpcServer, server)

	go func() {
		<-gracefulStop
		log.Debug("Received system signal, shutting down server")
		grpcServer.GracefulStop()
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal("Failed to serve",
			zap.String(logging.LogFieldError, err.Error()),
		)
	} else {
		log.Info("Server stopped")
	}
}
