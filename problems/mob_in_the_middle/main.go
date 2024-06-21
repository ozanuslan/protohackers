package main

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net"
	"os"
	"regexp"
)

const (
	upstreamServerAddr = "chat.protohackers.com:16963"
	tonyBogusCoinAddr  = "7YWHMfk9JZe0LM0g1ZauHuiSxhI"
)

var bogusCoinRegex *regexp.Regexp

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

	bogusCoinRegex = regexp.MustCompile(`^7[a-zA-Z0-9]{25,34}$`)
	bogusCoinRegex.Longest()

	for {
		c, err := l.Accept()
		if err != nil {
			log.Fatalln("Error while accepting connection:", err)
		}
		go handleConn(c)
	}
}

func handleConn(clientConn net.Conn) {
	clientAddr := clientConn.RemoteAddr().String()
	log.Println(clientAddr, "Accepted connection")
	defer func() {
		log.Println(clientAddr, "Closing connection")
		defer clientConn.Close()
	}()

	serverConn, err := net.Dial("tcp", upstreamServerAddr)
	if err != nil {
		log.Println(clientAddr, "Error while dialing upstream server:", err)
		return
	}
	defer serverConn.Close()

	log.Println("Proxying:", serverConn.RemoteAddr().String(), "<->", clientConn.RemoteAddr().String())
	go proxyMessages(serverConn, clientConn)
	proxyMessages(clientConn, serverConn)

	log.Println(clientAddr, "Session terminated")
}

func proxyMessages(from net.Conn, to net.Conn) {
	fromAddr := from.RemoteAddr().String()
	toAddr := to.RemoteAddr().String()

	bufRdr := bufio.NewReader(from)
	for {
		msg, err := bufRdr.ReadBytes('\n')
		if err == io.EOF {
			log.Println(fromAddr, "Reached EOF")
			return
		} else if err != nil {
			log.Println(fromAddr, "Error while reading conn:", err)
			return
		}
		msg = bytes.TrimRight(msg, "\n")
		log.Println(fromAddr, "->", toAddr, string(msg))

		split := bytes.Split(msg, []byte(" "))
		for i, word := range split {
			if bogusCoinRegex.Match(word) {
				split[i] = []byte(tonyBogusCoinAddr)
			}
		}
		msg = bytes.Join(split, []byte(" "))

		_, err = to.Write(append(msg, '\n'))
		if err != nil {
			log.Printf("%s Error while writing to (%s): %v\n", fromAddr, toAddr, err)
			return
		}
	}
}
