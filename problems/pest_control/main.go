package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
)

const (
	AuthorityServerHost = "pestcontrol.protohackers.com"
	AuthorityServerPort = "20547"
)

const (
	ProtocolString  = "pestcontrol"
	ProtocolVersion = 1
	MaxMsgLength    = 1024 * 1024 // bytes
)

const (
	MsgHello             = 0x50
	MsgError             = 0x51
	MsgOK                = 0x52
	MsgDialAuthority     = 0x53
	MsgTargetPopulations = 0x54
	MsgCreatePolicy      = 0x55
	MsgDeletePolicy      = 0x56
	MsgPolicyResult      = 0x57
	MsgSiteVisit         = 0x58
)

const (
	ActionCull     = 0x90
	ActionConserve = 0xa0
)

type Hello struct {
	Protocol string
	Version  uint32
}

type Error struct {
	Message string
}

type DialAuthority struct {
	Site uint32
}

type TargetPopulation struct {
	Species string
	Min     uint32
	Max     uint32
}

type TargetPopulations struct {
	Site        uint32
	Populations []TargetPopulation
}

type CreatePolicy struct {
	Species string
	Action  byte
}

type DeletePolicy struct {
	Policy uint32
}

type PolicyResult struct {
	Policy uint32
}

type PopulationObservation struct {
	Species string
	Count   uint32
}

type SiteVisit struct {
	Site        uint32
	Populations []PopulationObservation
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	ip, port := os.Getenv("IP"), os.Getenv("PORT")
	listenAddr := ip + ":" + port

	log.Println("Listening on", listenAddr)
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		panic(err)
	}
	defer l.Close()

	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatalln("Error while accepting connection:", err)
		}
		go handleConn(c)
	}
}

func handleConn(conn net.Conn) {
	remoteStr := "[" + conn.RemoteAddr().String() + "]"
	defer func() {
		log.Println(remoteStr, "Closing connection")
		conn.Close()
	}()

	log.Println(remoteStr, "Accepted connection")

	msgType, helloContent, err := readMessage(conn)
	if err != nil {
		if err != io.EOF {
			log.Printf("%s Error reading message: %v", remoteStr, err)
			if helloErr := sendHello(conn); helloErr != nil {
				log.Printf("%s Error sending Hello: %v", remoteStr, helloErr)
				return
			}
			sendError(conn, remoteStr, err.Error())
		}
		return
	}

	if err := sendHello(conn); err != nil {
		log.Printf("%s Error writing Hello response: %v", remoteStr, err)
		return
	}

	if msgType != MsgHello {
		sendError(conn, remoteStr, "First message must be Hello")
		return
	}

	reader := &byteReader{buf: helloContent}
	if err := validateHello(reader); err != nil {
		sendError(conn, remoteStr, err.Error())
		return
	}

	if reader.pos != len(helloContent) {
		sendError(conn, remoteStr, "Invalid Hello message")
		return
	}

	for { // SiteVisit message loop
		msgType, content, err := readMessage(conn)
		if err != nil {
			if err != io.EOF {
				log.Println(remoteStr, "Error reading message:", err)
			}
			return
		}

		if msgType != MsgSiteVisit {
			sendError(conn, remoteStr, fmt.Sprintf("Unexpected message type: %02x", msgType))
			return
		}

		site, observations, err := parseSiteVisit(content)
		if err != nil {
			// Map parse errors to appropriate error messages
			errorMsg := "Invalid SiteVisit message"
			errStr := err.Error()
			// Check for specific error messages that should be passed through
			if errStr == "unexpected trailing bytes" {
				errorMsg = "Invalid SiteVisit message"
			} else if len(errStr) > 23 && errStr[:23] == "conflicting counts for " {
				errorMsg = "Conflicting counts for species"
			}
			sendError(conn, remoteStr, errorMsg)
			return
		}

		if err := reconcilePolicies(site, observations); err != nil {
			log.Printf("%s Error reconciling policies for site %d: %v", remoteStr, site, err)
		}
	}
}

func readU32(r io.Reader) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func writeU32(buf []byte, val uint32) []byte {
	var valBuf [4]byte
	binary.BigEndian.PutUint32(valBuf[:], val)
	return append(buf, valBuf[:]...)
}

func readStr(r io.Reader) (string, error) {
	length, err := readU32(r)
	if err != nil {
		return "", err
	}
	if length > MaxMsgLength {
		return "", fmt.Errorf("string length too large: %d", length)
	}
	strBuf := make([]byte, length)
	if _, err := io.ReadFull(r, strBuf); err != nil {
		return "", err
	}
	return string(strBuf), nil
}

func writeStr(buf []byte, s string) []byte {
	buf = writeU32(buf, uint32(len(s)))
	return append(buf, []byte(s)...)
}

func calculateChecksum(data []byte) byte {
	sum := 0
	for _, b := range data {
		sum = (sum + int(b)) % 256
	}
	checksum := byte((256 - sum) % 256)
	return checksum
}

func readMessage(r io.Reader) (byte, []byte, error) {
	/*
		Message Layout:
		------------------------------------------------------------------
		| Type (1 byte) | Length (4 bytes) | Content | Checksum (1 byte) |
		------------------------------------------------------------------
	*/

	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, typeBuf); err != nil {
		return 0, nil, fmt.Errorf("error reading message type: %w", err)
	}
	msgType := typeBuf[0]

	length, err := readU32(r)
	if err != nil {
		return 0, nil, fmt.Errorf("error reading message length: %w", err)
	}

	if length < 6 {
		return 0, nil, fmt.Errorf("Invalid message length too short (length=%d)", length)
	}
	if length > MaxMsgLength {
		return 0, nil, fmt.Errorf("Invalid message length too large (length=%d)", length)
	}

	contentLen := int(length) - 6
	content := make([]byte, contentLen+1) // +1 for checksum
	if _, err := io.ReadFull(r, content); err != nil {
		return 0, nil, fmt.Errorf("error reading message content: %w", err)
	}

	allBytes := append(typeBuf, writeU32(nil, length)...)
	allBytes = append(allBytes, content...)
	expectedChecksum := calculateChecksum(allBytes)
	if expectedChecksum != 0 {
		return 0, nil, fmt.Errorf("Invalid checksum (sum=%d)", expectedChecksum)
	}

	return msgType, content[:len(content)-1], nil
}

func writeMessage(w io.Writer, msgType byte, content []byte) error {

	totalLength := 1 + 4 + len(content) + 1

	msg := make([]byte, 0, totalLength)
	msg = append(msg, msgType)
	msg = writeU32(msg, uint32(totalLength))
	msg = append(msg, content...)

	checksum := calculateChecksum(msg)
	msg = append(msg, checksum)

	written := 0
	for written < len(msg) {
		n, err := w.Write(msg[written:])
		if err != nil {
			return fmt.Errorf("error writing message (wrote %d of %d bytes): %w", written, len(msg), err)
		}
		written += n
	}
	return nil
}

var (
	targetPopulationsMu sync.RWMutex
	targetPopulations   = make(map[uint32][]TargetPopulation)

	sitePoliciesMu    sync.RWMutex
	sitePolicies      = make(map[uint32]map[string]uint32) // site -> species -> policy ID
	sitePolicyActions = make(map[uint32]map[string]byte)   // site -> species -> action

	authorityConnectionsMu sync.Mutex
	authorityConnections   = make(map[uint32]net.Conn)

	siteReconcileMu    sync.Mutex
	siteReconcileLocks = make(map[uint32]*sync.Mutex)
)

func sendHello(conn net.Conn) error {
	var helloContent []byte
	helloContent = writeStr(helloContent, ProtocolString)
	helloContent = writeU32(helloContent, 1)
	return writeMessage(conn, MsgHello, helloContent)
}

func sendError(conn net.Conn, remoteStr, message string) {
	errorContent := writeStr(nil, message)
	if err := writeMessage(conn, MsgError, errorContent); err != nil {
		log.Printf("%s Error writing Error message: %v", remoteStr, err)
	}
}

func validateHello(reader *byteReader) error {
	protocol, err := readStr(reader)
	if err != nil {
		return fmt.Errorf("reading protocol: %w", err)
	}
	if protocol != ProtocolString {
		return fmt.Errorf("invalid protocol: %s (expected %s)", protocol, ProtocolString)
	}

	version, err := readU32(reader)
	if err != nil {
		return fmt.Errorf("reading version: %w", err)
	}
	if version != ProtocolVersion {
		return fmt.Errorf("invalid version: %d (expected %d)", version, ProtocolVersion)
	}

	return nil
}

func parseSiteVisit(content []byte) (uint32, map[string]uint32, error) {
	reader := &byteReader{buf: content}

	site, err := readU32(reader)
	if err != nil {
		return 0, nil, fmt.Errorf("reading site: %w", err)
	}

	arrayLen, err := readU32(reader)
	if err != nil {
		return 0, nil, fmt.Errorf("reading array length: %w", err)
	}

	observations := make(map[string]uint32)
	for i := uint32(0); i < arrayLen; i++ {
		species, err := readStr(reader)
		if err != nil {
			return 0, nil, fmt.Errorf("reading species: %w", err)
		}

		count, err := readU32(reader)
		if err != nil {
			return 0, nil, fmt.Errorf("reading count: %w", err)
		}

		if existingCount, exists := observations[species]; exists && existingCount != count {
			return 0, nil, fmt.Errorf("conflicting counts for species %s", species)
		}

		observations[species] = count
	}

	if reader.pos != len(content) {
		return 0, nil, fmt.Errorf("unexpected trailing bytes")
	}

	return site, observations, nil
}

func connectToAuthority(site uint32) (net.Conn, []TargetPopulation, error) {
	address := net.JoinHostPort(AuthorityServerHost, AuthorityServerPort)
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, nil, err
	}

	// Close connection on any error, but not on success
	closeConn := true
	defer func() {
		if closeConn {
			conn.Close()
		}
	}()

	if err := sendHello(conn); err != nil {
		return nil, nil, err
	}

	msgType, content, err := readMessage(conn)
	if err != nil {
		return nil, nil, err
	}
	if msgType != MsgHello {
		return nil, nil, fmt.Errorf("expected Hello, got %02x", msgType)
	}

	reader := &byteReader{buf: content}
	if err := validateHello(reader); err != nil {
		return nil, nil, err
	}

	dialContent := writeU32(nil, site)
	if err := writeMessage(conn, MsgDialAuthority, dialContent); err != nil {
		return nil, nil, err
	}

	msgType, content, err = readMessage(conn)
	if err != nil {
		return nil, nil, err
	}
	if msgType != MsgTargetPopulations {
		return nil, nil, fmt.Errorf("expected TargetPopulations, got %02x", msgType)
	}

	reader = &byteReader{buf: content}
	siteID, err := readU32(reader)
	if err != nil {
		return nil, nil, err
	}
	if siteID != site {
		return nil, nil, fmt.Errorf("site ID mismatch: expected %d, got %d", site, siteID)
	}

	arrayLen, err := readU32(reader)
	if err != nil {
		return nil, nil, err
	}

	populations := make([]TargetPopulation, 0, arrayLen)
	for i := uint32(0); i < arrayLen; i++ {
		species, err := readStr(reader)
		if err != nil {
			return nil, nil, err
		}
		min, err := readU32(reader)
		if err != nil {
			return nil, nil, err
		}
		max, err := readU32(reader)
		if err != nil {
			return nil, nil, err
		}
		populations = append(populations, TargetPopulation{
			Species: species,
			Min:     min,
			Max:     max,
		})
	}

	// Success - don't close connection
	closeConn = false
	return conn, populations, nil
}

type byteReader struct {
	buf []byte
	pos int
}

func (r *byteReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.buf) {
		return 0, io.EOF
	}
	n = copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}

func ensureAuthorityConnection(site uint32) (net.Conn, error) {
	authorityConnectionsMu.Lock()
	defer authorityConnectionsMu.Unlock()

	if conn, exists := authorityConnections[site]; exists {
		return conn, nil
	}

	conn, populations, err := connectToAuthority(site)
	if err != nil {
		return nil, err
	}

	targetPopulationsMu.Lock()
	targetPopulations[site] = populations
	targetPopulationsMu.Unlock()

	authorityConnections[site] = conn

	return conn, nil
}

func sendCreatePolicy(conn net.Conn, species string, action byte) (uint32, error) {
	var content []byte
	content = writeStr(content, species)
	content = append(content, action)

	if err := writeMessage(conn, MsgCreatePolicy, content); err != nil {
		return 0, err
	}

	msgType, resultContent, err := readMessage(conn)
	if err != nil {
		return 0, err
	}
	if msgType != MsgPolicyResult {
		return 0, fmt.Errorf("expected PolicyResult, got %02x", msgType)
	}

	reader := &byteReader{buf: resultContent}
	policyID, err := readU32(reader)
	if err != nil {
		return 0, err
	}

	return policyID, nil
}

func sendDeletePolicy(conn net.Conn, policyID uint32) error {
	content := writeU32(nil, policyID)

	if err := writeMessage(conn, MsgDeletePolicy, content); err != nil {
		return err
	}

	msgType, _, err := readMessage(conn)
	if err != nil {
		return err
	}
	if msgType != MsgOK {
		return fmt.Errorf("expected OK, got %02x", msgType)
	}

	return nil
}

func getSiteMutex(site uint32) *sync.Mutex {
	siteReconcileMu.Lock()
	defer siteReconcileMu.Unlock()

	if mu, exists := siteReconcileLocks[site]; exists {
		return mu
	}

	mu := &sync.Mutex{}
	siteReconcileLocks[site] = mu
	return mu
}

func reconcilePolicies(site uint32, observations map[string]uint32) error {
	siteMu := getSiteMutex(site)
	siteMu.Lock()
	defer siteMu.Unlock()

	conn, err := ensureAuthorityConnection(site)
	if err != nil {
		return err
	}

	targetPopulationsMu.RLock()
	targets, exists := targetPopulations[site]
	targetPopulationsMu.RUnlock()

	if !exists {
		return fmt.Errorf("target populations not cached for site %d", site)
	}

	sitePoliciesMu.Lock()
	defer sitePoliciesMu.Unlock()

	policies := sitePolicies[site]
	if policies == nil {
		policies = make(map[string]uint32)
		sitePolicies[site] = policies
	}
	policyActions := sitePolicyActions[site]
	if policyActions == nil {
		policyActions = make(map[string]byte)
		sitePolicyActions[site] = policyActions
	}

	neededPolicies := make(map[string]byte) // species -> action
	for _, target := range targets {
		observedCount, observed := observations[target.Species]
		if !observed {
			observedCount = 0
		}

		if observedCount < target.Min {
			neededPolicies[target.Species] = ActionConserve
		} else if observedCount > target.Max {
			neededPolicies[target.Species] = ActionCull
		}
	}

	for species, policyID := range policies {
		neededAction, needed := neededPolicies[species]
		currentAction := policyActions[species]

		if !needed || currentAction != neededAction {
			if err := sendDeletePolicy(conn, policyID); err != nil {
				log.Printf("Error deleting policy %d for species %s at site %d: %v", policyID, species, site, err)
			} else {
				delete(policies, species)
				delete(policyActions, species)
			}
		}
	}

	for species, action := range neededPolicies {
		_, hasPolicy := policies[species]
		currentAction := policyActions[species]

		if !hasPolicy || (hasPolicy && currentAction != action) {
			policyID, err := sendCreatePolicy(conn, species, action)
			if err != nil {
				log.Printf("Error creating policy for species %s at site %d: %v", species, site, err)
				continue
			}

			policies[species] = policyID
			policyActions[species] = action
		}
	}

	return nil
}
