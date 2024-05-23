package main

import (
	"bytes"
	"log"
	"net"
	"os"
)

const version = "Ozan's KV Store v0.13.37"

var kv map[string]string
var conn *net.UDPConn

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	ip, port := os.Getenv("IP"), os.Getenv("PORT")
	listenAddr := ip + ":" + port
	udpAddr, _ := net.ResolveUDPAddr("udp", listenAddr)

	log.Println("Listening on", listenAddr)
	udpListener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		panic(err)
	}
	defer udpListener.Close()
	conn = udpListener

	kv = make(map[string]string)

	for {
		var buf [1000]byte
		n, addr, err := udpListener.ReadFrom(buf[:])
		if err != nil {
			log.Println(addr, "Error while reading packet:", err)
			continue
		}
		go handlePacket(addr.String(), buf[:n])
	}
}

func handlePacket(addr string, payload []byte) {
	log.Println(addr, "Received payload:", string(payload))
	if string(payload) == "version" {
		ver := "version=" + version
		log.Println(addr, "Sending version:", ver)
		err := sendPacket(addr, []byte(ver))
		if err != nil {
			log.Println(addr, "Failed to send version:", err)
		}
		return
	}

	switch true {
	case bytes.IndexByte(payload, '=') == -1: // Query
		key := string(payload)
		value, ok := kv[key]
		if !ok {
			log.Println(addr, "Key not found:", string(payload))
			return
		}
		log.Println(addr, "Query response:", key, "=", value)
		err := sendPacket(addr, []byte(key+"="+value))
		if err != nil {
			log.Println(addr, "Error while sending query response:", err)
		}
	default: // Insert
		split := bytes.SplitN(payload, []byte("="), 2)
		key := string(split[0])
		value := string(split[1])
		kv[key] = value
		log.Println(addr, "Inserted:", key, "=", value)
	}
}

func sendPacket(addr string, payload []byte) error {
	log.Println(addr, "Sending payload:", string(payload))
	remoteAddr, _ := net.ResolveUDPAddr("udp", addr)
	_, err := conn.WriteToUDP(payload, remoteAddr)
	return err
}
