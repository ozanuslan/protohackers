package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"math"
	"net"
	"os"
	"strconv"
)

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

	id := 0
	rdr := bufio.NewScanner(c)
	for {
		idStr := "[" + strconv.Itoa(id) + "]"

		var buf []byte
		for rdr.Scan() {
			buf = rdr.Bytes()
			break
		}

		err := rdr.Err()
		if err != nil {
			log.Println(remoteStr, idStr, "Error while reading connection:", err)
			log.Println(remoteStr, idStr, "Errored buffer:", buf)
			return
		}
		log.Println(remoteStr, idStr, "Request :", string(buf))
		buf = bytes.TrimSpace(buf)

		var input map[string]interface{}
		err = json.Unmarshal([]byte(buf), &input)
		if err != nil {
			log.Println(remoteStr, idStr, "Malformed JSON message:", err)
			break
		}

		m, ok := input["method"]
		if !ok {
			log.Println(remoteStr, idStr, "Method not present in message")
			break
		}

		isInvalidMethod := false
		var method string
		switch v := m.(type) {
		case string:
			method = v
		default:
			log.Println(remoteStr, idStr, "Unable to parse method:", m)
			isInvalidMethod = true
		}
		if isInvalidMethod {
			break
		}

		if method != "isPrime" {
			log.Println(remoteStr, idStr, "Method is not isPrime")
			break
		}

		p, ok := input["number"]
		if !ok {
			log.Println(remoteStr, idStr, "Number not present in message")
			break
		}

		var f float64
		isInvalidNumber := false
		switch i := p.(type) {
		case float64:
			f = i
		default:
			isInvalidNumber = true
			log.Println(remoteStr, idStr, "Unable to parse number:", p)
		}
		if isInvalidNumber {
			break
		}

		isPrimeNum := f == math.Trunc(f) && isPrime(int64(f))

		response := map[string]interface{}{
			"method": "isPrime",
			"prime":  isPrimeNum,
		}

		res, err := json.Marshal(response)
		if err != nil {
			log.Fatalln(remoteStr, idStr, "Unable to marshal response:", response)
		}
		log.Println(remoteStr, idStr, "Response:", string(res))

		_, err = c.Write(append(res, '\n'))
		if err != nil {
			log.Fatalln(remoteStr, idStr, "Error while writing connection:", err)
		}

		id++
	}

	log.Println(remoteStr, "Final Response: {}")
	c.Write([]byte("{}\n"))
}

func isPrime(n int64) bool {
	if n < 2 {
		return false
	}
	if n%2 == 0 {
		return n == 2
	}
	root := int64(math.Sqrt(float64(n)))
	for i := int64(3); i <= root; i += 2 {
		if n%i == 0 {
			return false
		}
	}
	return true
}
