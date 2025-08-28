// Copyright (c) 2024 TCP adapter implementation for GoVPP.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tcpclient

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"go.fd.io/govpp/adapter"
	"go.fd.io/govpp/binapi/memclnt"
	"go.fd.io/govpp/codec"
)

const (
	// DefaultTCPAddress is default VPP TCP API address.
	DefaultTCPAddress = "localhost:5002"
	// DefaultClientName is used for identifying client in socket registration
	DefaultClientName = "govpptcp"
)

var (
	// DefaultConnectTimeout is default timeout for connecting
	DefaultConnectTimeout = time.Second * 3
	// DefaultDisconnectTimeout is default timeout for disconnecting
	DefaultDisconnectTimeout = time.Millisecond * 100
	// DefaultKeepAlivePeriod is the default TCP keepalive period
	DefaultKeepAlivePeriod = time.Second * 30
)

var (
	debug       = strings.Contains(os.Getenv("DEBUG_GOVPP"), "tcpclient")
	debugMsgIds = strings.Contains(os.Getenv("DEBUG_GOVPP"), "msgtable")

	log logrus.FieldLogger
)

// SetLogger sets global logger.
func SetLogger(logger logrus.FieldLogger) {
	log = logger
}

func init() {
	logger := logrus.New()
	if debug {
		logger.Level = logrus.DebugLevel
		logger.Debug("govpp: debug level enabled for tcpclient")
	}
	log = logger.WithField("logger", "govpp/tcpclient")
}

type Client struct {
	address    string
	clientName string

	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer

	connectTimeout    time.Duration
	disconnectTimeout time.Duration
	keepAlivePeriod   time.Duration

	msgCallback  adapter.MsgCallback
	clientIndex  uint32
	msgTable     map[string]uint16
	msgTableMu   sync.RWMutex
	sockDelMsgId uint16
	writeMu      sync.Mutex

	headerPool *sync.Pool

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewVppClient returns a new Client using TCP connection.
// If address is empty string DefaultTCPAddress is used.
func NewVppClient(address string) *Client {
	if address == "" {
		address = DefaultTCPAddress
	}
	return &Client{
		address:           address,
		clientName:        DefaultClientName,
		connectTimeout:    DefaultConnectTimeout,
		disconnectTimeout: DefaultDisconnectTimeout,
		keepAlivePeriod:   DefaultKeepAlivePeriod,
		headerPool: &sync.Pool{New: func() interface{} {
			x := make([]byte, 16)
			return &x
		}},
		msgCallback: func(msgID uint16, data []byte) {
			log.Debugf("no callback set, dropping message: ID=%v len=%d", msgID, len(data))
		},
	}
}

// SetClientName sets a client name used for identification.
func (c *Client) SetClientName(name string) {
	c.clientName = name
}

// SetConnectTimeout sets timeout used during connecting.
func (c *Client) SetConnectTimeout(t time.Duration) {
	c.connectTimeout = t
}

// SetDisconnectTimeout sets timeout used during disconnecting.
func (c *Client) SetDisconnectTimeout(t time.Duration) {
	c.disconnectTimeout = t
}

// SetKeepAlivePeriod sets TCP keepalive period.
func (c *Client) SetKeepAlivePeriod(t time.Duration) {
	c.keepAlivePeriod = t
}

// SetMsgCallback sets the callback for incoming messages.
func (c *Client) SetMsgCallback(cb adapter.MsgCallback) {
	log.Debug("SetMsgCallback")
	c.msgCallback = cb
}

// WaitReady is implemented for compatibility but returns immediately for TCP.
// TCP connections don't need to wait for a socket file to appear.
func (c *Client) WaitReady() error {
	// TCP doesn't need to wait for socket files
	return nil
}

func (c *Client) Connect() error {
	if err := c.connect(); err != nil {
		return err
	}

	if err := c.open(c.clientName); err != nil {
		_ = c.disconnect()
		return err
	}

	c.quit = make(chan struct{})
	c.wg.Add(1)
	go c.readerLoop()

	return nil
}

func (c *Client) Disconnect() error {
	if c.conn == nil {
		return nil
	}
	log.Debugf("Disconnecting..")

	close(c.quit)

	// For TCP, we close the connection which will cause the reader to exit
	if tcpConn, ok := c.conn.(*net.TCPConn); ok {
		if err := tcpConn.CloseRead(); err != nil {
			log.Debugf("closing read failed: %v", err)
		}
	}

	// wait for readerLoop to return
	c.wg.Wait()

	if err := c.close(); err != nil {
		log.Debugf("closing failed: %v", err)
	}

	if err := c.disconnect(); err != nil {
		return err
	}

	return nil
}

const defaultBufferSize = 4096

func (c *Client) connect() error {
	if debug {
		log.Debugf("Connecting to TCP: %v", c.address)
	}

	// Parse TCP address
	tcpAddr, err := net.ResolveTCPAddr("tcp", c.address)
	if err != nil {
		return fmt.Errorf("invalid TCP address %s: %v", c.address, err)
	}

	// Connect with timeout
	dialer := &net.Dialer{
		Timeout: c.connectTimeout,
	}
	conn, err := dialer.Dial("tcp", tcpAddr.String())
	if err != nil {
		log.Debugf("Connecting to TCP %s failed: %s", c.address, err)
		return fmt.Errorf("TCP connection to %s failed: %v", c.address, err)
	}

	// Set TCP keepalive for connection health
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		if err := tcpConn.SetKeepAlive(true); err != nil {
			log.Debugf("Failed to set keepalive: %v", err)
		}
		if err := tcpConn.SetKeepAlivePeriod(c.keepAlivePeriod); err != nil {
			log.Debugf("Failed to set keepalive period: %v", err)
		}
		// Disable Nagle's algorithm for lower latency
		if err := tcpConn.SetNoDelay(true); err != nil {
			log.Debugf("Failed to set TCP_NODELAY: %v", err)
		}
	}

	c.conn = conn
	if debug {
		log.Debugf("Connected to TCP (local addr: %v, remote addr: %v)", 
			c.conn.LocalAddr(), c.conn.RemoteAddr())
	}

	c.reader = bufio.NewReaderSize(c.conn, defaultBufferSize)
	c.writer = bufio.NewWriterSize(c.conn, defaultBufferSize)

	return nil
}

func (c *Client) disconnect() error {
	log.Debugf("Closing TCP connection")

	// cleanup msg table
	c.setMsgTable(make(map[string]uint16), 0)

	if err := c.conn.Close(); err != nil {
		log.Debugln("Closing TCP connection failed:", err)
		return err
	}
	return nil
}

const (
	sockCreateMsgId  = 15 // hard-coded sockclnt_create message ID
	createMsgContext = byte(123)
	deleteMsgContext = byte(124)
)

func (c *Client) open(clientName string) error {
	var msgCodec = codec.DefaultCodec

	// Request socket client create
	req := &memclnt.SockclntCreate{
		Name: clientName,
	}
	msg, err := msgCodec.EncodeMsg(req, sockCreateMsgId)
	if err != nil {
		log.Debugln("Encode error:", err)
		return err
	}
	// set non-0 context
	msg[5] = createMsgContext

	if err := c.writeMsg(msg); err != nil {
		log.Debugln("Write error: ", err)
		return err
	}
	msgReply, err := c.readMsgTimeout(nil, c.connectTimeout)
	if err != nil {
		log.Println("Read error:", err)
		return err
	}

	reply := new(memclnt.SockclntCreateReply)
	if err := msgCodec.DecodeMsg(msgReply, reply); err != nil {
		log.Println("Decoding sockclnt_create_reply failed:", err)
		return err
	} else if reply.Response != 0 {
		return fmt.Errorf("sockclnt_create_reply: response error (%d)", reply.Response)
	}

	log.Debugf("SockclntCreateReply: Response=%v Index=%v Count=%v",
		reply.Response, reply.Index, reply.Count)

	c.clientIndex = reply.Index
	msgTable := make(map[string]uint16, reply.Count)

	for i := uint16(0); i < reply.Count; i++ {
		x, err := c.readMsgTimeout(nil, c.connectTimeout)
		if err != nil {
			return err
		}

		strx := string(x)
		if strings.Contains(strx, "sockclnt_delete_") {
			c.sockDelMsgId = getMsgId(x)
		}
		msgName, msgCrc := getMsgNameCrc(x)
		msgTable[msgName+"_"+msgCrc] = getMsgId(x)

		if debugMsgIds {
			log.Debugf(" #%03d: %80q %s", getMsgId(x), msgName+"_"+msgCrc, x)
		}
	}

	if debug {
		log.Debugf("Loaded %d message definitions", len(msgTable))
	}

	c.setMsgTable(msgTable, c.sockDelMsgId)

	return nil
}

func (c *Client) close() error {
	var msgCodec = codec.DefaultCodec

	req := &memclnt.SockclntDelete{
		Index: c.clientIndex,
	}
	msg, err := msgCodec.EncodeMsg(req, c.sockDelMsgId)
	if err != nil {
		log.Debugln("Encode error:", err)
		return err
	}

	// set non-0 context
	msg[5] = deleteMsgContext

	log.Debugf("sending socklnt_delete (%d bytes): % 0X", len(msg), msg)
	if err := c.writeMsg(msg); err != nil {
		log.Debugln("Write error: ", err)
		return err
	}

	msgReply, err := c.readMsgTimeout(nil, c.disconnectTimeout)
	if err != nil {
		log.Warnln("Read timeout:", err)
		return err
	} else if debug {
		log.Debugf("received socklnt_delete_reply (%d bytes): % 0X", len(msgReply), msgReply)
	}

	reply := new(memclnt.SockclntDeleteReply)
	if err := msgCodec.DecodeMsg(msgReply, reply); err != nil {
		log.Debugln("Decoding sockclnt_delete_reply failed:", err)
		return err
	} else if reply.Response != 0 {
		return fmt.Errorf("sockclnt_delete_reply: response error (%d)", reply.Response)
	}

	return nil
}

func (c *Client) setMsgTable(msgTable map[string]uint16, sockDelMsgId uint16) {
	c.msgTableMu.Lock()
	defer c.msgTableMu.Unlock()

	c.msgTable = msgTable
	c.sockDelMsgId = sockDelMsgId
}

func (c *Client) GetMsgID(msgName string, msgCrc string) (uint16, error) {
	c.msgTableMu.RLock()
	defer c.msgTableMu.RUnlock()

	if msgID, ok := c.msgTable[msgName+"_"+msgCrc]; ok {
		return msgID, nil
	}
	return 0, &adapter.UnknownMsgError{
		MsgName: msgName,
		MsgCrc:  msgCrc,
	}
}

func (c *Client) SendMsg(context uint32, data []byte) error {
	if len(data) < 10 {
		return fmt.Errorf("invalid message data, length must be at least 10 bytes")
	}
	
	// Check if we're connected
	if c.conn == nil || c.writer == nil {
		return fmt.Errorf("not connected")
	}
	
	setMsgRequestHeader(data, c.clientIndex, context)

	if debug {
		log.Debugf("sendMsg (%d) context=%v client=%d: % 02X", len(data), context, c.clientIndex, data)
	}

	if err := c.writeMsg(data); err != nil {
		log.Debugln("writeMsg error: ", err)
		return err
	}

	return nil
}

// setMsgRequestHeader sets client index and context in the message request header
func setMsgRequestHeader(data []byte, clientIndex, context uint32) {
	// message ID is already set
	binary.BigEndian.PutUint32(data[2:6], clientIndex)
	binary.BigEndian.PutUint32(data[6:10], context)
}

func (c *Client) writeMsg(msg []byte) error {
	// we lock to prevent mixing multiple message writes
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header, ok := c.headerPool.Get().(*[]byte)
	if !ok {
		return fmt.Errorf("failed to get header from pool")
	}
	err := writeMsgHeader(c.writer, *header, len(msg))
	if err != nil {
		return err
	}
	c.headerPool.Put(header)

	if err := writeMsgData(c.writer, msg, c.writer.Size()); err != nil {
		return err
	}

	if err := c.writer.Flush(); err != nil {
		return err
	}

	if debug {
		log.Debugf(" -- writeMsg done")
	}

	return nil
}

func writeMsgHeader(w io.Writer, header []byte, dataLen int) error {
	binary.BigEndian.PutUint32(header[8:12], uint32(dataLen))

	n, err := w.Write(header)
	if err != nil {
		return err
	}
	if debug {
		log.Debugf(" - header sent (%d/%d): % 0X", n, len(header), header)
	}

	return nil
}

func writeMsgData(w io.Writer, msg []byte, writerSize int) error {
	for i := 0; i <= len(msg)/writerSize; i++ {
		x := i*writerSize + writerSize
		if x > len(msg) {
			x = len(msg)
		}
		if debug {
			log.Debugf(" - x=%v i=%v len=%v mod=%v", x, i, len(msg), len(msg)/writerSize)
		}
		n, err := w.Write(msg[i*writerSize : x])
		if err != nil {
			return err
		}
		if debug {
			log.Debugf(" - data sent x=%d (%d/%d): % 0X", x, n, len(msg), msg)
		}
	}
	return nil
}

func (c *Client) readerLoop() {
	defer c.wg.Done()
	defer log.Debugf("reader loop done")

	var buf [8192]byte

	for {
		select {
		case <-c.quit:
			return
		default:
		}

		msg, err := c.readMsg(buf[:])
		if err != nil {
			if isClosedError(err) {
				log.Debugf("reader closed: %v", err)
				return
			}
			log.Debugf("readMsg error: %v", err)
			continue
		}

		msgID, context := getMsgReplyHeader(msg)
		if debug {
			log.Debugf("readerLoop recv msg: msgID=%d context=%v len=%d", msgID, context, len(msg))
		}

		c.msgCallback(msgID, msg)
	}
}

func getMsgReplyHeader(msg []byte) (msgID uint16, context uint32) {
	msgID = binary.BigEndian.Uint16(msg[0:2])
	context = binary.BigEndian.Uint32(msg[2:6])
	return
}

func (c *Client) readMsgTimeout(buf []byte, timeout time.Duration) ([]byte, error) {
	// set read deadline
	readDeadline := time.Now().Add(timeout)
	if err := c.conn.SetReadDeadline(readDeadline); err != nil {
		return nil, err
	}

	// read message
	msgReply, err := c.readMsg(buf)
	if err != nil {
		return nil, err
	}

	// reset read deadline
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}

	return msgReply, nil
}

func (c *Client) readMsg(buf []byte) ([]byte, error) {
	header := make([]byte, 16)

	n, err := io.ReadAtLeast(c.reader, header, 16)
	if err != nil {
		return nil, err
	} else if n == 0 {
		return nil, io.EOF
	}

	if debug {
		log.Debugf(" - read header %d bytes: % 0X", n, header)
	}

	dataLen := binary.BigEndian.Uint32(header[8:12])

	if debug {
		log.Debugf(" - decoded header: dataLen=%d", dataLen)
	}

	if dataLen > 0 {
		// Allocate buffer if needed
		var msg []byte
		if buf == nil || len(buf) < int(dataLen) {
			msg = make([]byte, dataLen)
		} else {
			msg = buf[:dataLen]
		}

		// Use io.ReadFull to ensure we read all the data bytes
		n, err := io.ReadFull(c.reader, msg)
		if err != nil {
			return nil, err
		}
		if n != int(dataLen) {
			return nil, fmt.Errorf("invalid message length: expected %d, got %d", dataLen, n)
		}

		if debug {
			log.Debugf(" - read data %d bytes: % 0X", dataLen, msg[:min(100, int(dataLen))])
		}

		return msg, nil
	}

	return nil, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func getMsgId(msg []byte) uint16 {
	return binary.BigEndian.Uint16(msg[0:2])
}

func getMsgNameCrc(msg []byte) (name string, crc string) {
	if len(msg) < 5 {
		return "", ""
	}
	nameLen := binary.BigEndian.Uint16(msg[3:5])
	if len(msg) < int(5+nameLen) {
		return "", ""
	}
	name = string(msg[5 : 5+nameLen])
	n := strings.Index(name, "_")
	if n > 0 {
		crc = name[n+1:]
		name = name[:n]
	}
	return
}

func isClosedError(err error) bool {
	if err == io.EOF {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	if strings.Contains(err.Error(), "use of closed network connection") {
		return true
	}
	return false
}
