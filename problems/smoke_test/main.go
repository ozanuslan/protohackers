package main

import (
	"io"
	"log"
	"net"
	"os"
)

func main() {
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
	defer c.Close()

	remote := "[" + c.RemoteAddr().String() + "]"
	log.Println(remote, "Accepted connection")

	buf, err := io.ReadAll(c)
	if err != nil {
		log.Fatalln(remote, "Error while reading connection:", err)
	}
	log.Println(remote, "Read msg of len:", len(buf))

	_, err = c.Write(buf)
	if err != nil {
		log.Fatalln(remote, "Error while writing connection:", err)
	}
	log.Println(remote, "Echoed back")

	log.Println(remote, "Closing connection")
}
