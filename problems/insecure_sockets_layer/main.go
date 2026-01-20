package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
)

const (
	MaxCipherSpecSize = 80
	MaxLineLength     = 5000
	NewlineByte       = 0x0A
)

type CipherOp struct {
	Type    byte
	Operand byte
}

type Session struct {
	conn        net.Conn
	cipherSpec  []CipherOp
	clientPos   int64
	serverPos   int64
	inputBuffer []byte
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

func handleConn(c net.Conn) {
	remoteStr := "[" + c.RemoteAddr().String() + "]"
	defer func() {
		log.Println(remoteStr, "Closing connection")
		c.Close()
	}()
	log.Println(remoteStr, "Accepted connection")

	reader := bufio.NewReader(c)
	cipherSpecBytes := make([]byte, 0, MaxCipherSpecSize)
	for len(cipherSpecBytes) < MaxCipherSpecSize {
		b, err := reader.ReadByte()
		if err != nil {
			log.Println(remoteStr, "Error reading cipher spec:", err)
			return
		}
		cipherSpecBytes = append(cipherSpecBytes, b)
		if b == 0x00 { // 0x00 marks end of cipher spec
			break
		}
	}

	cipherSpec, err := parseCipherSpec(cipherSpecBytes)
	if err != nil {
		log.Println(remoteStr, "Error parsing cipher spec:", err)
		return
	}

	if isNoOp(cipherSpec) {
		log.Println(remoteStr, "No-op cipher detected, disconnecting")
		return
	}

	session := &Session{
		conn:        c,
		cipherSpec:  cipherSpec,
		clientPos:   0,
		serverPos:   0,
		inputBuffer: make([]byte, 0),
	}

	processSession(session, reader, remoteStr)
}

func parseCipherSpec(data []byte) ([]CipherOp, error) {
	var ops []CipherOp
	i := 0
	for i < len(data) {
		if data[i] == 0x00 {
			break
		}

		switch data[i] {
		case 0x01: // reversebits: reverse bit order (LSB ↔ MSB)
			ops = append(ops, CipherOp{Type: 0x01})
			i++
		case 0x02: // xor(N): XOR byte by value N (next byte is operand)
			if i+1 >= len(data) {
				return nil, fmt.Errorf("xor operation missing operand")
			}
			ops = append(ops, CipherOp{Type: 0x02, Operand: data[i+1]})
			i += 2
		case 0x03: // xorpos: XOR byte by its position in stream
			ops = append(ops, CipherOp{Type: 0x03})
			i++
		case 0x04: // add(N): Add N to byte, modulo 256 (next byte is operand)
			if i+1 >= len(data) {
				return nil, fmt.Errorf("add operation missing operand")
			}
			ops = append(ops, CipherOp{Type: 0x04, Operand: data[i+1]})
			i += 2
		case 0x05: // addpos: Add position to byte, modulo 256
			ops = append(ops, CipherOp{Type: 0x05})
			i++
		default:
			return nil, fmt.Errorf("unknown cipher operation: %02x", data[i])
		}
	}

	return ops, nil
}

func reverseBits(b byte) byte {
	// Reverse bit order: LSB becomes MSB, 2nd LSB becomes 2nd MSB, etc.
	var result byte
	for i := 0; i < 8; i++ {
		result <<= 1           // Shift result left to make room for next bit
		result |= (b >> i) & 1 // Extract bit i from input and set it in result
	}
	return result
}

func encodeByte(b byte, ops []CipherOp, pos int64) byte {
	// Apply cipher operations in forward order (encoding)
	result := b
	for _, op := range ops {
		switch op.Type {
		case 0x01: // reversebits: reverse bit order
			result = reverseBits(result)
		case 0x02: // xor(N): XOR by operand value
			result ^= op.Operand
		case 0x03: // xorpos: XOR by stream position
			result ^= byte(pos)
		case 0x04: // add(N): Add operand, wrap at 256
			result = byte((int(result) + int(op.Operand)) % 256)
		case 0x05: // addpos: Add position, wrap at 256
			result = byte((int(result) + int(pos)) % 256)
		}
	}
	return result
}

func decodeByte(b byte, ops []CipherOp, pos int64) byte {
	// Apply inverse cipher operations in reverse order (decoding)
	result := b
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.Type {
		case 0x01: // reversebits: self-inverse (reverse again)
			result = reverseBits(result)
		case 0x02: // xor(N): self-inverse (XOR again with same value)
			result ^= op.Operand
		case 0x03: // xorpos: self-inverse (XOR again with position)
			result ^= byte(pos)
		case 0x04: // add(N): inverse is subtract (with wrap handling)
			result = byte((int(result) - int(op.Operand) + 256) % 256)
		case 0x05: // addpos: inverse is subtract position (with wrap handling)
			result = byte((int(result) - int(pos) + 256) % 256)
		}
	}
	return result
}

func isNoOp(ops []CipherOp) bool {
	if len(ops) == 0 {
		return true
	}
	hasPosOp := false
	for _, op := range ops {
		if op.Type == 0x03 || op.Type == 0x05 {
			hasPosOp = true
			break
		}
	}

	positions := []int64{0}
	if hasPosOp {
		positions = []int64{0, 1, 2, 10, 100}
	}

	for _, pos := range positions {
		for testByte := 0; testByte < 256; testByte++ {
			encoded := encodeByte(byte(testByte), ops, pos)
			if encoded != byte(testByte) {
				return false
			}
		}
	}
	return true
}

func parseToyRequest(line string) (string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("empty request")
	}

	parts := strings.Split(line, ",")
	maxQuantity := -1
	maxToy := ""

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		xIdx := strings.Index(part, "x")
		if xIdx == -1 {
			return "", fmt.Errorf("invalid format: no 'x' found")
		}

		quantityStr := strings.TrimSpace(part[:xIdx])
		quantity, err := strconv.Atoi(quantityStr)
		if err != nil {
			return "", fmt.Errorf("invalid quantity: %s", quantityStr)
		}

		if quantity < 0 {
			return "", fmt.Errorf("negative quantity")
		}

		toyName := strings.TrimSpace(part[xIdx+1:])
		if toyName == "" {
			return "", fmt.Errorf("empty toy name")
		}

		if quantity > maxQuantity {
			maxQuantity = quantity
			maxToy = part
		}
	}

	if maxToy == "" {
		return "", fmt.Errorf("no valid toys found")
	}

	return maxToy, nil
}

func processSession(session *Session, reader *bufio.Reader, remoteStr string) {
	buf := make([]byte, 4096)
	decodedBuffer := make([]byte, 0, MaxLineLength)

	readDone := false
	for !readDone {
		n, err := reader.Read(buf)
		if err != nil {
			if err == io.EOF {
				readDone = true
			} else {
				log.Println(remoteStr, "Error reading from connection:", err)
				return
			}
		}
		if n > 0 {
			session.inputBuffer = append(session.inputBuffer, buf[:n]...)
		}

		for len(session.inputBuffer) > 0 {
			obfuscatedByte := session.inputBuffer[0]
			session.inputBuffer = session.inputBuffer[1:]

			decodedByte := decodeByte(obfuscatedByte, session.cipherSpec, session.clientPos)
			session.clientPos++
			decodedBuffer = append(decodedBuffer, decodedByte)

			if decodedByte == NewlineByte {
				lineStr := string(decodedBuffer[:len(decodedBuffer)-1])
				decodedBuffer = decodedBuffer[:0]

				if len(lineStr) > MaxLineLength {
					log.Println(remoteStr, "Line too long, disconnecting")
					return
				}

				maxToy, err := parseToyRequest(lineStr)
				if err != nil {
					log.Println(remoteStr, "Error parsing request:", err)
					return
				}

				response := maxToy + "\n"
				responseBytes := []byte(response)

				encodedResponse := make([]byte, len(responseBytes))
				for i, b := range responseBytes {
					encodedResponse[i] = encodeByte(b, session.cipherSpec, session.serverPos)
					session.serverPos++
				}

				_, err = session.conn.Write(encodedResponse)
				if err != nil {
					log.Println(remoteStr, "Error writing response:", err)
					return
				}
			} else {
				if len(decodedBuffer) > MaxLineLength {
					log.Println(remoteStr, "Decoded line too long, disconnecting")
					return
				}
			}
		}
	}
}
