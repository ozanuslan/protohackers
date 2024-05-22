package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
)

type Message struct {
	Op   byte
	Int1 int32
	Int2 int32
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
		defer conn.Close()
	}()
	log.Println(remoteStr, "Accepted connection")

	db := make(map[int32]int32)
	for {
		var chunk [9]byte
		n, err := io.ReadFull(conn, chunk[:])
		if err == io.EOF {
			log.Println(remoteStr, "Reached EOF")
			break
		}
		if err != nil {
			log.Println(remoteStr, "Error while reading chunk:", err)
		}
		if n != 9 {
			log.Println(remoteStr, "Invalid chunk length:", n)
			break
		}

		msg := parseMessage([9]byte(chunk))
		log.Println(remoteStr, fmt.Sprintf("Message : {Op:%c,Int1:%d,Int2:%d}", msg.Op, msg.Int1, msg.Int2))

		invalidOp := false
		switch msg.Op {
		case 'I':
			handleInsert(db, &msg)
		case 'Q':
			mean := handleQuery(db, &msg)
			log.Println(remoteStr, "Mean    :", mean)
			conn.Write(binary.BigEndian.AppendUint32(nil, uint32(mean)))
		default:
			log.Println(remoteStr, "Invalid operation type received:", string(msg.Op))
			conn.Write([]byte("UNDEFINED"))
			invalidOp = true
		}
		if invalidOp {
			break
		}
	}
}

func handleInsert(db map[int32]int32, msg *Message) {
	timestamp, price := msg.Int1, msg.Int2
	db[timestamp] = price
}

func handleQuery(db map[int32]int32, msg *Message) int32 {
	minTime, maxTime := msg.Int1, msg.Int2
	if len(db) == 0 || minTime > maxTime {
		return 0
	}

	mean := float64(0)
	elements := 1
	for ts, price := range db {
		if ts >= minTime && ts <= maxTime {
			mean += (float64(price) - mean) / float64(elements)
			elements++
		}
	}
	return int32(math.Round(mean))
}

func parseMessage(req [9]byte) Message {
	msg := Message{}
	msg.Op = req[0]
	msg.Int1 = int32(binary.BigEndian.Uint32(req[1:5]))
	msg.Int2 = int32(binary.BigEndian.Uint32(req[5:]))
	return msg
}
