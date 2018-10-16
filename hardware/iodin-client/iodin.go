package iodin

import (
	"net"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/juju/errors"
)

//go:generate protoc -I=../../protobuf --go_out=./ ../../protobuf/iodin.proto

type Client struct {
	proc *os.Process
	sock *net.UnixConn
}

func NewClient(path string) (*Client, error) {
	fds, err := syscall.Socketpair(syscall.AF_LOCAL, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, errors.Trace(err)
	}

	clientFile := os.NewFile(uintptr(fds[1]), "iodin-fd-client")
	defer clientFile.Close()
	sockClient, err := net.FileConn(clientFile)
	if err != nil {
		return nil, errors.Trace(err)
	}
	sock := sockClient.(*net.UnixConn)

	attr := &os.ProcAttr{
		Env: []string{"sock_fd=" + strconv.FormatUint(uint64(fds[0]), 10)},
	}
	p, err := os.StartProcess(path, nil, attr)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return &Client{proc: p, sock: sock}, nil
}

func (self *Client) Do(request *Request, response *Response) error {
	requestBytes, err := proto.Marshal(request)
	if err != nil {
		return errors.Annotatef(err, "iodin.Do.Marshal req=%s", request.String())
	}

	self.sock.SetDeadline(time.Now().Add(time.Second))
	defer self.sock.SetDeadline(time.Time{})
	_, err = self.sock.Write(requestBytes)
	if err != nil {
		return errors.Annotatef(err, "iodin.Do.Send req=%s", request.String())
	}
	responseBuf := make([]byte, 256)
	n, err := self.sock.Read(responseBuf)
	if err != nil {
		return errors.Annotatef(err, "iodin.Do.Recv req=%s", request.String())
	}
	responseBytes := responseBuf[:n]

	err = proto.Unmarshal(responseBytes, response)
	if err != nil {
		return errors.Annotatef(err, "iodin.Do.Unmarshal req=%s recv=%x", request.String(), responseBytes)
	}
	return nil
}
