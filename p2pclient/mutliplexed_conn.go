package p2pclient

import (
	"fmt"
	"sync"

	ggio "github.com/gogo/protobuf/io"
	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p-core/protocol"
	pb "github.com/libp2p/go-libp2p-daemon/pb"
	manet "github.com/multiformats/go-multiaddr/net"
)

type MultiplexedConn interface {
	WriteRequest(req *pb.Request) error
	ReadUnaryRequest(proto protocol.ID) (*pb.Response, error)
	ReadUnaryResponse(callID uuid.UUID) (*pb.Response, error)
}

type multiplexedConn struct {
	wm sync.Mutex

	writer ggio.Writer
	reader ggio.ReadCloser

	handleTasks sync.Map
	callResults sync.Map
}

var _ MultiplexedConn = (*multiplexedConn)(nil)

func NewMultiplexedConn(conn manet.Conn, messageSizeMax int) *multiplexedConn {
	control := &multiplexedConn{
		writer: ggio.NewDelimitedWriter(conn),
		reader: ggio.NewDelimitedReader(conn, messageSizeMax),
	}

	go control.listen()

	return control
}

func (mc *multiplexedConn) listen() {
	defer mc.reader.Close()
	for {
		msg := &pb.Response{}
		if err := mc.reader.ReadMsg(msg); err != nil {
			log.Debugw("failed to read message from connection", err)
			return
		}

		log.Debugw("received message from daemon", msg)

		if msg.RequestHandling != nil {
			go mc.doHandleRequest(msg)
		} else if msg.CallUnaryResponse != nil {
			go mc.doReturnResponse(msg)
		}
	}
}

// WriteRequest writes request to persistent connection
func (mc *multiplexedConn) WriteRequest(req *pb.Request) error {
	mc.wm.Lock()
	defer mc.wm.Unlock()

	return mc.writer.WriteMsg(req)
}

// ReadUnaryRequest locks until a handle request is sent to the client, then
// returns the request to the client
func (mc *multiplexedConn) ReadUnaryRequest(proto protocol.ID) (*pb.Response, error) {
	cn, _ := mc.handleTasks.LoadOrStore(proto, make(chan *pb.Response))
	reqChan := cn.(chan *pb.Response)

	return <-reqChan, nil
}

// ReadUnaryResponse locks until a response to a given call id is sent to
// the persistent connection, then returns this response
func (mc *multiplexedConn) ReadUnaryResponse(callID uuid.UUID) (*pb.Response, error) {
	cn := make(chan *pb.Response)
	mc.callResults.Store(callID, cn)
	defer mc.callResults.Delete(callID)

	return <-cn, nil
}

func responseErrorProtoNotFound(callID []byte, p protocol.ID) *pb.Request {
	errMsg := []byte(fmt.Sprintf("protocol %s not supported", p))
	return &pb.Request{
		Type: pb.Request_SEND_RESPONSE_TO_REMOTE.Enum(),
		SendResponseToRemote: &pb.CallUnaryResponse{
			Result: make([]byte, 0),
			Error:  errMsg,
			CallId: callID,
		},
	}
}

func (mc *multiplexedConn) doHandleRequest(msg *pb.Response) {
	protoID := protocol.ID(*msg.RequestHandling.Proto)
	hc, found := mc.handleTasks.Load(protoID)
	if !found {
		fmt.Println("not found")
		mc.writer.WriteMsg(
			responseErrorProtoNotFound(
				msg.RequestHandling.CallId,
				protoID,
			),
		)
	}
	handeChan := hc.(chan *pb.Response)
	handeChan <- msg
}

func (mc *multiplexedConn) doReturnResponse(msg *pb.Response) {
	callID, err := uuid.FromBytes(msg.CallUnaryResponse.CallId)
	// TODO: clean this mess up
	if err != nil {
		panic(err)
	}

	cr, found := mc.callResults.Load(callID)
	if !found {
		return
	}

	callResults := cr.(chan *pb.Response)
	callResults <- msg
}
