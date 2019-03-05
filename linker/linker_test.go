package linker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Axway/ace-golang-sdk/linker/rpcclient"
	"github.com/Axway/ace-golang-sdk/messaging"
	"github.com/Axway/ace-golang-sdk/rpc"
	"google.golang.org/grpc/metadata"
)

var testProcBusMsg *messaging.BusinessMessage

func testProc(c context.Context, bm *messaging.BusinessMessage, mp MsgProducer) error {
	testProcBusMsg = bm
	return nil
}

func TestRegister(t *testing.T) {

	testServiceName := "test-service"
	testServiceVersion := "test-version"
	testDescription := "test-descr"

	link, err := Register(testServiceName, testServiceVersion, testDescription, testProc)

	if link == nil && err != nil {
		t.Errorf("did not expect error from call to Register with valid parameters")
	}
	if link.MsgProcessor == nil {
		t.Errorf("message processor not assigned")
	}

	if link.name != testServiceName {
		t.Errorf("incorrect description, expected %s got %s", testServiceName, link.name)
	}
	if link.version != testServiceVersion {
		t.Errorf("incorrect description, expected %s got %s", testServiceVersion, link.version)
	}
	if link.description != testDescription {
		t.Errorf("incorrect description, expected %s got %s", testDescription, link.description)
	}
}

type mockClient struct {
	buildClientRelayCalled bool
	clientRegisterCalled   bool
}
type mockStream struct {
	closeSendCalled bool
}

func (m mockStream) CloseSend() error {
	m.closeSendCalled = true
	return nil
}

/*
type Linkage_RelayClient interface {
	Send(*Message) error
	Recv() (*Receipt, error)
	grpc.ClientStream
}
*/
type linkageRelayClient struct{}

var closeSendCalled bool
var sentMessage *rpc.Message

func (l linkageRelayClient) CloseSend() error { //grpc.ClientStream interface method
	fmt.Printf("CloseSend called")
	closeSendCalled = true
	return nil
}
func (l linkageRelayClient) Context() context.Context     { return context.Background() }
func (l linkageRelayClient) Header() (metadata.MD, error) { return nil, nil }
func (l linkageRelayClient) Trailer() metadata.MD         { return nil }
func (l linkageRelayClient) Send(m *rpc.Message) error {
	fmt.Printf("Send of rpc.Message called")
	sentMessage = m
	return nil
}
func (l linkageRelayClient) Recv() (*rpc.Receipt, error) { return nil, io.EOF }
func (l linkageRelayClient) SendMsg(m interface{}) error { return nil }
func (l linkageRelayClient) RecvMsg(m interface{}) error { return nil }

// rpcClient implements RPCClient interface
func (mc *mockClient) BuildClientRelay(clientContext context.Context, aceMsg *rpc.Message, host string, port uint16) (*rpcclient.LinkerClientRelay, error) {
	// real one does this:
	//return rpcclient.BuildClientRelay(clientContext, aceMsg, host, port)
	mc.buildClientRelayCalled = true
	lcr := rpcclient.LinkerClientRelay{
		Stream:        linkageRelayClient{},
		SourceMessage: aceMsg,
	}
	return &lcr, nil
}

// rpcClient implements RPCClient interface
func (mc *mockClient) ClientRegister(host string, port uint16, serviceInfo *rpc.ServiceInfo) (bool, error) {
	// real one:
	//return rpcclient.ClientRegister(host, port, serviceInfo)

	mc.clientRegisterCalled = true
	return true, nil
}

func TestOnSidecarRegistrationComplete(t *testing.T) {
	mockClient := &mockClient{}

	link := &Link{
		client: mockClient,
	}

	serviceInfo := rpc.ServiceInfo{
		ServiceName:    "abc",
		ServiceVersion: "1.0.0",
	}
	err := link.OnSidecarRegistrationComplete(&serviceInfo)

	if err != nil {
		t.Errorf("no error was expected, but got: %v", err)
	}

	if !mockClient.clientRegisterCalled {
		t.Errorf("OnSidecarRegistrationComplete should have called ClientRegister")
	}
}

func TestOnRelayNoErrors(t *testing.T) {
	mockClient := &mockClient{}

	link := &Link{
		client:       mockClient,
		MsgProcessor: BusinessMessageProcessor(testProc),
	}

	thisConsumptionID := "100"

	aceMsg := &rpc.Message{
		Pattern: &rpc.StepPattern{
			ServiceName:    "abc",
			ServiceVersion: "1.0.0",
		},
		BusinessMessage: &messaging.BusinessMessage{
			Payload: &messaging.Payload{
				Body: []byte("test"),
			},
		},
		Consumption_ID: thisConsumptionID,
	}

	link.OnRelay(aceMsg)

	if !closeSendCalled {
		t.Errorf("expected CloseSend on LinkerClientRelay to have been called")
	}

	//was the testProc called with aceMsg.GetBusinessMessage()?
	if testProcBusMsg == nil {
		t.Errorf("testProc function should have saved its argument to testProcBusMsg")
	}
	if string(testProcBusMsg.Payload.Body) != "test" {
		t.Errorf("testProc function should have been called with BusinessMessage.Payload.Body: %v but got %v", []byte("test"), testProcBusMsg.Payload.Body)
	}
	if sentMessage != nil {
		t.Errorf("BusinessMessageProcessor function is normally responsible for calling Stream.Send and since we did not set testProc to do that, " +
			"non-null sentMessage indicates a condition of incorrect setup")
	}
}

func testProcReturnProcessingError(c context.Context, bm *messaging.BusinessMessage, mp MsgProducer) error {
	testProcBusMsg = bm
	return NewProcessingError(errors.New("test-processing-error"))
}
func TestOnRelayProcessingError(t *testing.T) {
	mockClient := &mockClient{}

	link := &Link{
		client:       mockClient,
		MsgProcessor: BusinessMessageProcessor(testProcReturnProcessingError),
	}

	thisConsumptionID := "200"

	aceMsg := &rpc.Message{
		Pattern: &rpc.StepPattern{
			ServiceName:    "abc",
			ServiceVersion: "1.0.0",
		},
		BusinessMessage: &messaging.BusinessMessage{
			Payload: &messaging.Payload{
				Body: []byte("test"),
			},
		},
		Consumption_ID: thisConsumptionID,
	}
	if aceMsg.GetErrorType() != rpc.Message_NONE {
		t.Errorf("ace message has incorrect ErrorType, expected the default value of NONE, got: %v", aceMsg.GetErrorType())
	}

	link.OnRelay(aceMsg)

	if !closeSendCalled {
		t.Errorf("expected CloseSend on LinkerClientRelay to have been called")
	}

	// aceMsg is passed as source message when BuildClientRelay is called, so we can look for error type on it here. Alternatively,
	// we can test if we saved it as 'sentMessage' which will both show us that:
	// a) Send was called (otherwise sentMessage will be nil)
	// b) test sentMessage to carry error information
	if aceMsg.GetErrorType() != rpc.Message_PROCESSING {
		t.Errorf("when BusinessMessageProcessor function returns ProcessingError, it should be present in message, but got: %v",
			aceMsg.GetErrorType())
	}

	//test Send was called by getting its message argument
	if sentMessage == nil {
		t.Errorf("unable to examine message saved from call to Stream.Send method")
	}

	if sentMessage.GetConsumption_ID() != thisConsumptionID {
		t.Errorf("sanity check: wrong message saved from call to Stream.Send method")
	}

	if sentMessage.GetErrorType() != rpc.Message_PROCESSING {
		t.Errorf("when BusinessMessageProcessor function returns ProcessingError, it should be present in sentMessage, but got: %v",
			sentMessage.GetErrorType())
	}
}
