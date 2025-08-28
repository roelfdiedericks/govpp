// Copyright (c) 2024 Shared memory adapter implementation for GoVPP.
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

package shmclient

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"

	"go.fd.io/govpp/adapter"
	"go.fd.io/govpp/binapi/memclnt"
	"go.fd.io/govpp/codec"
)

const (
	// DefaultSHMPrefix is the default prefix for shared memory segments
	DefaultSHMPrefix = "/dev/shm/vpp_api_"
	// DefaultSHMSize is the default size of shared memory segment
	DefaultSHMSize = 64 * 1024 * 1024 // 64MB
	// DefaultClientName is used for identifying client
	DefaultClientName = "govppshm"
	// RingBufferHeaderSize is the size of ring buffer header
	RingBufferHeaderSize = 64
	// MessageHeaderSize is the size of message header
	MessageHeaderSize = 16
	// MaxMessageSize is the maximum message size
	MaxMessageSize = 4096
)

var (
	// DefaultConnectTimeout is default timeout for connecting
	DefaultConnectTimeout = time.Second * 3
	// DefaultDisconnectTimeout is default timeout for disconnecting
	DefaultDisconnectTimeout = time.Millisecond * 100
	// DefaultPollInterval is the default polling interval
	DefaultPollInterval = time.Microsecond * 100
)

var (
	debug       = strings.Contains(os.Getenv("DEBUG_GOVPP"), "shmclient")
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
		logger.Debug("govpp: debug level enabled for shmclient")
	}
	log = logger.WithField("logger", "govpp/shmclient")
}

// RingBuffer represents a lock-free ring buffer in shared memory
type RingBuffer struct {
	data       []byte
	head       *uint32 // Points to head in shared memory
	tail       *uint32 // Points to tail in shared memory
	size       uint32
	dataOffset uint32
}

// Client represents shared memory VPP API client
type Client struct {
	shmName    string
	clientName string
	
	shmFile *os.File
	shmMem  []byte
	shmSize int

	txRing *RingBuffer
	rxRing *RingBuffer

	connectTimeout    time.Duration
	disconnectTimeout time.Duration
	pollInterval      time.Duration

	msgCallback  adapter.MsgCallback
	clientIndex  uint32
	msgTable     map[string]uint16
	msgTableMu   sync.RWMutex
	sockDelMsgId uint16

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewVppClient returns a new Client using shared memory.
// If shmName is empty string "default" is used.
func NewVppClient(shmName string) *Client {
	if shmName == "" {
		shmName = "default"
	}
	return &Client{
		shmName:           shmName,
		clientName:        DefaultClientName,
		connectTimeout:    DefaultConnectTimeout,
		disconnectTimeout: DefaultDisconnectTimeout,
		pollInterval:      DefaultPollInterval,
		shmSize:           DefaultSHMSize,
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

// SetPollInterval sets the polling interval for checking messages.
func (c *Client) SetPollInterval(t time.Duration) {
	c.pollInterval = t
}

// SetMsgCallback sets the callback for incoming messages.
func (c *Client) SetMsgCallback(cb adapter.MsgCallback) {
	log.Debug("SetMsgCallback")
	c.msgCallback = cb
}

// WaitReady waits for the shared memory segment to be available.
func (c *Client) WaitReady() error {
	shmPath := c.getSHMPath()
	
	// Check if shared memory already exists
	if _, err := os.Stat(shmPath); err == nil {
		return nil // Already exists
	}
	
	// Wait for shared memory to appear
	timeout := time.After(c.connectTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for shared memory segment: %s", shmPath)
		case <-ticker.C:
			if _, err := os.Stat(shmPath); err == nil {
				return nil
			}
		}
	}
}

func (c *Client) Connect() error {
	if err := c.openSHM(); err != nil {
		return err
	}

	if err := c.initRingBuffers(); err != nil {
		c.closeSHM()
		return err
	}

	if err := c.open(c.clientName); err != nil {
		c.closeSHM()
		return err
	}

	c.quit = make(chan struct{})
	c.wg.Add(1)
	go c.pollLoop()

	return nil
}

func (c *Client) Disconnect() error {
	if c.shmMem == nil {
		return nil
	}
	log.Debugf("Disconnecting..")

	close(c.quit)
	c.wg.Wait()

	if err := c.close(); err != nil {
		log.Debugf("closing failed: %v", err)
	}

	if err := c.closeSHM(); err != nil {
		return err
	}

	return nil
}

func (c *Client) getSHMPath() string {
	return DefaultSHMPrefix + c.shmName
}

func (c *Client) openSHM() error {
	shmPath := c.getSHMPath()
	
	if debug {
		log.Debugf("Opening shared memory: %v", shmPath)
	}

	// Open or create shared memory file
	file, err := os.OpenFile(shmPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open shared memory %s: %v", shmPath, err)
	}

	// Get file info to check size
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("failed to stat shared memory: %v", err)
	}

	// Resize if necessary
	if info.Size() < int64(c.shmSize) {
		if err := file.Truncate(int64(c.shmSize)); err != nil {
			file.Close()
			return fmt.Errorf("failed to resize shared memory: %v", err)
		}
	}

	// Memory map the file
	mmap, err := syscall.Mmap(
		int(file.Fd()),
		0,
		c.shmSize,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED,
	)
	if err != nil {
		file.Close()
		return fmt.Errorf("failed to mmap shared memory: %v", err)
	}

	c.shmFile = file
	c.shmMem = mmap

	if debug {
		log.Debugf("Mapped shared memory: %d bytes", len(c.shmMem))
	}

	return nil
}

func (c *Client) closeSHM() error {
	log.Debugf("Closing shared memory")

	// cleanup msg table
	c.setMsgTable(make(map[string]uint16), 0)

	if c.shmMem != nil {
		if err := syscall.Munmap(c.shmMem); err != nil {
			log.Debugf("Failed to munmap: %v", err)
		}
		c.shmMem = nil
	}

	if c.shmFile != nil {
		if err := c.shmFile.Close(); err != nil {
			log.Debugf("Failed to close shm file: %v", err)
		}
		c.shmFile = nil
	}

	return nil
}

func (c *Client) initRingBuffers() error {
	// Simple layout: first half for TX, second half for RX
	halfSize := len(c.shmMem) / 2
	
	c.txRing = c.createRingBuffer(c.shmMem[:halfSize])
	c.rxRing = c.createRingBuffer(c.shmMem[halfSize:])
	
	if debug {
		log.Debugf("Initialized ring buffers: TX=%d bytes, RX=%d bytes", halfSize, halfSize)
	}
	
	return nil
}

func (c *Client) createRingBuffer(mem []byte) *RingBuffer {
	rb := &RingBuffer{
		data:       mem,
		size:       uint32(len(mem) - RingBufferHeaderSize),
		dataOffset: RingBufferHeaderSize,
	}
	
	// Head and tail pointers are at the beginning of the buffer
	rb.head = (*uint32)(unsafe.Pointer(&mem[0]))
	rb.tail = (*uint32)(unsafe.Pointer(&mem[4]))
	
	// Initialize to zero
	atomic.StoreUint32(rb.head, 0)
	atomic.StoreUint32(rb.tail, 0)
	
	return rb
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
	
	msgReply, err := c.readMsgTimeout(c.connectTimeout)
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
		x, err := c.readMsgTimeout(c.connectTimeout)
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

	msgReply, err := c.readMsgTimeout(c.disconnectTimeout)
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

	if c.txRing == nil {
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
	return c.txRing.Enqueue(msg)
}

func (c *Client) readMsg() ([]byte, error) {
	return c.rxRing.Dequeue()
}

func (c *Client) readMsgTimeout(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	
	for {
		if msg, err := c.readMsg(); err == nil && msg != nil {
			return msg, nil
		}
		
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("read timeout")
		}
		
		<-ticker.C
	}
}

func (c *Client) pollLoop() {
	defer c.wg.Done()
	defer log.Debugf("poll loop done")

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.quit:
			return
		case <-ticker.C:
			// Check for incoming messages
			msg, err := c.readMsg()
			if err != nil {
				if debug {
					log.Debugf("readMsg error: %v", err)
				}
				continue
			}
			if msg == nil {
				continue
			}

			msgID, context := getMsgReplyHeader(msg)
			if debug {
				log.Debugf("pollLoop recv msg: msgID=%d context=%v len=%d", msgID, context, len(msg))
			}

			c.msgCallback(msgID, msg)
		}
	}
}

// Enqueue adds a message to the ring buffer (lock-free)
func (rb *RingBuffer) Enqueue(data []byte) error {
	dataLen := uint32(len(data))
	totalLen := dataLen + 4 // 4 bytes for length prefix
	
	if totalLen > rb.size {
		return fmt.Errorf("message too large: %d > %d", totalLen, rb.size)
	}
	
	for {
		head := atomic.LoadUint32(rb.head)
		tail := atomic.LoadUint32(rb.tail)
		
		// Calculate available space
		var available uint32
		if head >= tail {
			available = rb.size - (head - tail)
		} else {
			available = tail - head
		}
		
		if available < totalLen {
			return fmt.Errorf("ring buffer full")
		}
		
		// Try to claim space
		newHead := (head + totalLen) % rb.size
		if !atomic.CompareAndSwapUint32(rb.head, head, newHead) {
			continue // Retry if someone else modified head
		}
		
		// Write length prefix
		offset := rb.dataOffset + head
		binary.BigEndian.PutUint32(rb.data[offset:], dataLen)
		
		// Write data
		copy(rb.data[offset+4:], data)
		
		return nil
	}
}

// Dequeue removes a message from the ring buffer (lock-free)
func (rb *RingBuffer) Dequeue() ([]byte, error) {
	for {
		head := atomic.LoadUint32(rb.head)
		tail := atomic.LoadUint32(rb.tail)
		
		// Check if empty
		if head == tail {
			return nil, nil // Empty buffer
		}
		
		// Read length prefix
		offset := rb.dataOffset + tail
		if offset+4 > uint32(len(rb.data)) {
			return nil, fmt.Errorf("invalid offset")
		}
		dataLen := binary.BigEndian.Uint32(rb.data[offset:])
		
		if dataLen == 0 || dataLen > MaxMessageSize {
			return nil, fmt.Errorf("invalid message length: %d", dataLen)
		}
		
		totalLen := dataLen + 4
		
		// Read data
		data := make([]byte, dataLen)
		copy(data, rb.data[offset+4:offset+4+dataLen])
		
		// Update tail
		newTail := (tail + totalLen) % rb.size
		if !atomic.CompareAndSwapUint32(rb.tail, tail, newTail) {
			continue // Retry if someone else modified tail
		}
		
		return data, nil
	}
}

func getMsgReplyHeader(msg []byte) (msgID uint16, context uint32) {
	msgID = binary.BigEndian.Uint16(msg[0:2])
	context = binary.BigEndian.Uint32(msg[2:6])
	return
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
