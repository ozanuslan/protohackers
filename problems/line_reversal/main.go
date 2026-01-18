package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	RetransmissionTimeout = 3 * time.Second
	SessionExpiryTimeout  = 60 * time.Second
	MaxMessageSize        = 1000
	MaxNumericValue       = 2147483648
	NewlineByte           = 0x0A
)

type MessageType string

const (
	MsgConnect MessageType = "connect"
	MsgData    MessageType = "data"
	MsgAck     MessageType = "ack"
	MsgClose   MessageType = "close"
)

type LRCPMessage struct {
	Type    MessageType
	Session int64
	Pos     int64
	Length  int64
	Data    []byte // unescaped for data messages
}

type Session struct {
	ID                   int64
	RemoteAddr           *net.UDPAddr
	InboundChunks        map[int64][]byte // position -> data chunks (unescaped)
	InboundLength        int64            // total received length (contiguous from 0)
	ProcessedLength      int64            // bytes already processed by application
	OutboundBuffer       []byte           // data to send (before escaping)
	OutboundLength       int64            // total sent length
	OutboundPos          int64            // position of next data to send
	AckedLength          int64            // acknowledged by peer
	MaxReceivedAckLength int64            // largest ACK length received from peer (for duplicate detection)
	retransmitTimer      *time.Timer
	ExpiryTimer          *time.Timer
	mu                   sync.RWMutex
	conn                 *net.UDPConn
}

var (
	sessions   map[int64]*Session
	sessionsMu sync.RWMutex
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	ip, port := os.Getenv("IP"), os.Getenv("PORT")
	listenAddr := ip + ":" + port
	udpAddr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		panic(err)
	}

	udpListener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		panic(err)
	}
	defer udpListener.Close()

	sessions = make(map[int64]*Session)

	for {
		var buf [1000]byte
		n, addr, err := udpListener.ReadFromUDP(buf[:])
		if err != nil {
			log.Println(addr, "Error while reading packet:", err)
			continue
		}
		go handlePacket(udpListener, addr, buf[:n])
	}
}

func handlePacket(conn *net.UDPConn, addr *net.UDPAddr, payload []byte) {
	if len(payload) >= MaxMessageSize {
		return
	}

	msg, err := parseMessage(payload)
	if err != nil {
		return
	}

	log.Printf("[%d] Received message type: %s from %s", msg.Session, msg.Type, addr)

	switch msg.Type {
	case MsgConnect:
		handleConnect(conn, addr, msg)
	case MsgData:
		handleData(conn, addr, msg)
	case MsgAck:
		handleAck(conn, addr, msg)
	case MsgClose:
		handleClose(conn, addr, msg)
	default:
	}
}

func parseMessage(payload []byte) (*LRCPMessage, error) {
	msg := &LRCPMessage{}

	if len(payload) < 2 || payload[0] != '/' || payload[len(payload)-1] != '/' {
		return nil, fmt.Errorf("invalid message format")
	}

	content := payload[1 : len(payload)-1]

	parseField := func(start int) (field []byte, nextPos int, err error) {
		var result []byte
		i := start
		for i < len(content) {
			if content[i] == '\\' && i+1 < len(content) {
				result = append(result, content[i], content[i+1])
				i += 2
			} else if content[i] == '/' {
				return result, i + 1, nil
			} else {
				result = append(result, content[i])
				i++
			}
		}
		return result, i, nil
	}

	typeField, pos, err := parseField(0)
	if err != nil {
		return nil, err
	}
	if len(typeField) == 0 {
		return nil, fmt.Errorf("empty message type")
	}
	msg.Type = MessageType(typeField)

	switch msg.Type {
	case MsgConnect:
		sessionField, nextPos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		if nextPos < len(content) {
			return nil, fmt.Errorf("connect message has extra fields")
		}
		session, err := parseNumeric(string(sessionField))
		if err != nil {
			return nil, err
		}
		msg.Session = session

	case MsgData:
		sessionField, pos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		session, err := parseNumeric(string(sessionField))
		if err != nil {
			return nil, err
		}
		msg.Session = session

		posField, pos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		posVal, err := parseNumeric(string(posField))
		if err != nil {
			return nil, err
		}
		msg.Pos = posVal

		if pos < len(content) {
			i := pos
			for i < len(content) {
				if content[i] == '\\' && i+1 < len(content) {
					i += 2
				} else if content[i] == '/' {
					return nil, fmt.Errorf("data message has unescaped slash in data field")
				} else {
					i++
				}
			}
			msg.Data = unescapeData(content[pos:])
		} else {
			msg.Data = []byte{}
		}

	case MsgAck:
		sessionField, pos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		session, err := parseNumeric(string(sessionField))
		if err != nil {
			return nil, err
		}
		msg.Session = session

		lengthField, nextPos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		if nextPos < len(content) {
			return nil, fmt.Errorf("ack message has extra fields")
		}
		length, err := parseNumeric(string(lengthField))
		if err != nil {
			return nil, err
		}
		msg.Length = length

	case MsgClose:
		sessionField, nextPos, err := parseField(pos)
		if err != nil {
			return nil, err
		}
		if nextPos < len(content) {
			return nil, fmt.Errorf("close message has extra fields")
		}
		session, err := parseNumeric(string(sessionField))
		if err != nil {
			return nil, err
		}
		msg.Session = session

	default:
		return nil, fmt.Errorf("unknown message type")
	}

	return msg, nil
}

func parseNumeric(s string) (int64, error) {
	val, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if val < 0 || val >= MaxNumericValue {
		return 0, fmt.Errorf("numeric value out of range")
	}
	return val, nil
}

// unescapeData processes escape sequences: \\ -> \ and \/ -> /
// All other bytes pass through unchanged (including literal \ followed by other chars)
func unescapeData(escaped []byte) []byte {
	var result []byte
	i := 0
	for i < len(escaped) {
		if escaped[i] == '\\' && i+1 < len(escaped) {
			switch escaped[i+1] {
			case '\\':
				result = append(result, '\\')
				i += 2
			case '/':
				result = append(result, '/')
				i += 2
			default:
				result = append(result, '\\')
				i++
			}
		} else {
			result = append(result, escaped[i])
			i++
		}
	}
	return result
}

// escapeData escapes \ and / for sending in data messages
func escapeData(data []byte) []byte {
	var result []byte
	for _, b := range data {
		switch b {
		case '\\':
			result = append(result, '\\', '\\')
		case '/':
			result = append(result, '\\', '/')
		default:
			result = append(result, b)
		}
	}
	return result
}

func getSession(sessionID int64) *Session {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()
	return sessions[sessionID]
}

func getOrCreateSession(sessionID int64, remoteAddr *net.UDPAddr, conn *net.UDPConn) *Session {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	session, exists := sessions[sessionID]
	if !exists {
		session = &Session{
			ID:            sessionID,
			RemoteAddr:    remoteAddr,
			InboundChunks: make(map[int64][]byte),
			conn:          conn,
		}
		sessions[sessionID] = session
		log.Printf("[%d] Created new session from %s", sessionID, remoteAddr)
	} else {
		session.mu.Lock()
		session.RemoteAddr = remoteAddr
		session.mu.Unlock()
	}
	return session
}

func deleteSession(sessionID int64) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if session, exists := sessions[sessionID]; exists {
		if session.retransmitTimer != nil {
			session.retransmitTimer.Stop()
		}
		if session.ExpiryTimer != nil {
			session.ExpiryTimer.Stop()
		}
		delete(sessions, sessionID)
	}
}

func handleConnect(conn *net.UDPConn, addr *net.UDPAddr, msg *LRCPMessage) {
	session := getOrCreateSession(msg.Session, addr, conn)
	session.mu.Lock()
	defer session.mu.Unlock()

	if session.ExpiryTimer != nil {
		session.ExpiryTimer.Stop()
	}
	session.ExpiryTimer = time.AfterFunc(SessionExpiryTimeout, func() {
		deleteSession(session.ID)
	})

	sendAck(conn, addr, msg.Session, 0)
}

func handleData(conn *net.UDPConn, addr *net.UDPAddr, msg *LRCPMessage) {
	session := getSession(msg.Session)
	if session == nil {
		sendClose(conn, addr, msg.Session)
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.ExpiryTimer != nil {
		session.ExpiryTimer.Stop()
	}
	session.ExpiryTimer = time.AfterFunc(SessionExpiryTimeout, func() {
		log.Printf("[%d] Session expired after %v", session.ID, SessionExpiryTimeout)
		deleteSession(session.ID)
	})

	if msg.Pos > session.InboundLength {
		log.Printf("[%d] Out of order data: POS %d > InboundLength %d", msg.Session, msg.Pos, session.InboundLength)
		sendAck(conn, addr, msg.Session, session.InboundLength)
		return
	}

	if msg.Pos < session.InboundLength {
		log.Printf("[%d] Duplicate data: POS %d < InboundLength %d", msg.Session, msg.Pos, session.InboundLength)
		sendAck(conn, addr, msg.Session, session.InboundLength)
		return
	}

	if len(msg.Data) > 0 {
		session.InboundChunks[msg.Pos] = msg.Data
		session.InboundLength += int64(len(msg.Data))
	}

	processInboundStream(session)

	sendAck(conn, addr, msg.Session, session.InboundLength)
}

func processInboundStream(session *Session) {
	var buffer []byte
	pos := int64(0)
	for pos < session.InboundLength {
		chunk, exists := session.InboundChunks[pos]
		if !exists {
			break
		}
		chunkStart := pos
		chunkEnd := pos + int64(len(chunk))

		processStart := chunkStart
		if session.ProcessedLength > chunkStart {
			processStart = session.ProcessedLength
		}

		if processStart < chunkEnd {
			offset := processStart - chunkStart
			unprocessedData := chunk[offset:]
			buffer = append(buffer, unprocessedData...)
		}

		pos = chunkEnd
	}

	log.Printf("[%d] Processing inbound stream: bufferLen=%d, ProcessedLength=%d, InboundLength=%d",
		session.ID, len(buffer), session.ProcessedLength, session.InboundLength)

	linesProcessed := 0
	for len(buffer) > 0 {
		newlineIdx := bytes.IndexByte(buffer, NewlineByte)
		if newlineIdx == -1 {
			break
		}

		line := buffer[:newlineIdx]
		reversed := reverseBytes(line)
		output := append(reversed, NewlineByte)

		session.OutboundBuffer = append(session.OutboundBuffer, output...)

		log.Printf("[%d] Processed line: input=%q, output=%q", session.ID, line, output)

		bytesProcessed := newlineIdx + 1
		buffer = buffer[bytesProcessed:]
		session.ProcessedLength += int64(bytesProcessed)
		linesProcessed++
	}

	if linesProcessed > 0 {
		log.Printf("[%d] Processed %d lines, OutboundBuffer now has %d bytes",
			session.ID, linesProcessed, len(session.OutboundBuffer))
	}

	if len(session.OutboundBuffer) > 0 {
		log.Printf("[%d] Calling sendOutboundData with %d bytes in buffer", session.ID, len(session.OutboundBuffer))
		sendOutboundData(session)
	}
}

func reverseBytes(data []byte) []byte {
	result := make([]byte, len(data))
	for i := 0; i < len(data); i++ {
		result[i] = data[len(data)-1-i]
	}
	return result
}

func sendOutboundData(session *Session) {
	log.Printf("[%d] sendOutboundData: OutboundPos=%d, OutboundBufferLen=%d, OutboundLength=%d",
		session.ID, session.OutboundPos, len(session.OutboundBuffer), session.OutboundLength)

	for session.OutboundPos < int64(len(session.OutboundBuffer)) {
		prefixLen := len(fmt.Sprintf("/data/%d/%d/", session.ID, session.OutboundPos))
		suffixLen := 1
		maxEscapedDataLen := MaxMessageSize - prefixLen - suffixLen

		if maxEscapedDataLen <= 0 {
			return
		}

		remainingData := session.OutboundBuffer[session.OutboundPos:]

		maxUnescapedLen := len(remainingData)
		if maxUnescapedLen > maxEscapedDataLen/2 {
			maxUnescapedLen = maxEscapedDataLen / 2
		}

		bestLen := 0
		for testLen := 1; testLen <= maxUnescapedLen && testLen <= len(remainingData); testLen++ {
			testData := remainingData[:testLen]
			escapedData := escapeData(testData)
			testMessage := fmt.Sprintf("/data/%d/%d/%s/", session.ID, session.OutboundPos, string(escapedData))
			if len(testMessage) < MaxMessageSize {
				bestLen = testLen
			} else {
				break
			}
		}

		if bestLen == 0 {
			return
		}

		dataToSend := remainingData[:bestLen]
		escapedData := escapeData(dataToSend)
		escapedDataStr := string(escapedData)
		message := fmt.Sprintf("/data/%d/%d/%s/", session.ID, session.OutboundPos, escapedDataStr)

		_, err := session.conn.WriteToUDP([]byte(message), session.RemoteAddr)
		if err != nil {
			log.Printf("[%d] Error sending data: %v", session.ID, err)
			return
		}

		dataLen := int64(bestLen)
		session.OutboundLength += dataLen
		session.OutboundPos += dataLen

		if session.retransmitTimer != nil {
			session.retransmitTimer.Stop()
		}
		session.retransmitTimer = time.AfterFunc(RetransmissionTimeout, func() {
			session.mu.Lock()
			defer session.mu.Unlock()
			if session.OutboundPos > session.AckedLength {
				sendOutboundDataRetransmit(session)
			}
		})
	}
}

func sendOutboundDataRetransmit(session *Session) {
	retransmitPos := session.AckedLength

	for retransmitPos < session.OutboundPos {
		remainingData := session.OutboundBuffer[retransmitPos:session.OutboundPos]

		prefixLen := len(fmt.Sprintf("/data/%d/%d/", session.ID, retransmitPos))
		suffixLen := 1
		maxEscapedDataLen := MaxMessageSize - prefixLen - suffixLen

		if maxEscapedDataLen <= 0 {
			break
		}

		maxUnescapedLen := len(remainingData)
		if maxUnescapedLen > maxEscapedDataLen/2 {
			maxUnescapedLen = maxEscapedDataLen / 2
		}

		bestLen := 0
		for testLen := 1; testLen <= maxUnescapedLen && testLen <= len(remainingData); testLen++ {
			testData := remainingData[:testLen]
			escapedData := escapeData(testData)
			testMessage := fmt.Sprintf("/data/%d/%d/%s/", session.ID, retransmitPos, string(escapedData))
			if len(testMessage) < MaxMessageSize {
				bestLen = testLen
			} else {
				break
			}
		}

		if bestLen == 0 {
			break
		}

		dataToSend := remainingData[:bestLen]
		escapedData := escapeData(dataToSend)
		escapedDataStr := string(escapedData)
		message := fmt.Sprintf("/data/%d/%d/%s/", session.ID, retransmitPos, escapedDataStr)

		_, err := session.conn.WriteToUDP([]byte(message), session.RemoteAddr)
		if err != nil {
			log.Printf("[%d] Error retransmitting data: %v", session.ID, err)
			break
		}

		retransmitPos += int64(bestLen)
	}

	if session.retransmitTimer != nil {
		session.retransmitTimer.Stop()
	}
	if session.OutboundPos > session.AckedLength {
		session.retransmitTimer = time.AfterFunc(RetransmissionTimeout, func() {
			session.mu.Lock()
			defer session.mu.Unlock()
			if session.OutboundPos > session.AckedLength {
				sendOutboundDataRetransmit(session)
			}
		})
	}
}

func handleAck(conn *net.UDPConn, addr *net.UDPAddr, msg *LRCPMessage) {
	session := getSession(msg.Session)
	if session == nil {
		// Session not open, send close
		log.Printf("[%d] ACK message received but session not found", msg.Session)
		sendClose(conn, addr, msg.Session)
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	log.Printf("[%d] Handling ACK: Length=%d, MaxReceivedAckLength=%d, OutboundLength=%d, OutboundPos=%d",
		msg.Session, msg.Length, session.MaxReceivedAckLength, session.OutboundLength, session.OutboundPos)

	if session.ExpiryTimer != nil {
		session.ExpiryTimer.Stop()
	}
	session.ExpiryTimer = time.AfterFunc(SessionExpiryTimeout, func() {
		log.Printf("[%d] Session expired after %v", session.ID, SessionExpiryTimeout)
		deleteSession(session.ID)
	})

	if msg.Length <= session.MaxReceivedAckLength {
		return
	}

	if msg.Length > session.OutboundLength {
		log.Printf("[%d] Misbehaving peer: ACK length %d > sent length %d", session.ID, msg.Length, session.OutboundLength)
		sessionID := session.ID
		deleteSession(sessionID)
		sendClose(conn, addr, sessionID)
		return
	}

	session.AckedLength = msg.Length
	session.MaxReceivedAckLength = msg.Length

	if session.AckedLength >= session.OutboundPos {
		if session.retransmitTimer != nil {
			session.retransmitTimer.Stop()
			session.retransmitTimer = nil
		}
	}

	if msg.Length < session.OutboundLength {
		sendOutboundDataRetransmit(session)
	}

	if len(session.OutboundBuffer) > 0 && session.OutboundPos < int64(len(session.OutboundBuffer)) {
		sendOutboundData(session)
	}
}

func handleClose(conn *net.UDPConn, addr *net.UDPAddr, msg *LRCPMessage) {
	sendClose(conn, addr, msg.Session)

	session := getSession(msg.Session)
	if session != nil {
		deleteSession(msg.Session)
	}
}

func sendAck(conn *net.UDPConn, addr *net.UDPAddr, sessionID int64, length int64) {
	message := fmt.Sprintf("/ack/%d/%d/", sessionID, length)
	_, err := conn.WriteToUDP([]byte(message), addr)
	if err != nil {
		log.Printf("[%d] Error sending ack: %v", sessionID, err)
	}
}

func sendClose(conn *net.UDPConn, addr *net.UDPAddr, sessionID int64) {
	message := fmt.Sprintf("/close/%d/", sessionID)
	_, err := conn.WriteToUDP([]byte(message), addr)
	if err != nil {
		log.Printf("[%d] Error sending close: %v", sessionID, err)
	}
}
