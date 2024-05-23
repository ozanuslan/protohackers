package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
)

type Client struct {
	Name string
	Conn net.Conn
}

type Room struct {
	Clients map[string]*Client
}

var room *Room

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

	room = &Room{Clients: make(map[string]*Client)}

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

	client := Client{Conn: conn, Name: ""}
	err := client.PromptName()
	if err != nil {
		log.Println(remoteStr, "Error while getting client name:", err)
		client.Conn.Write([]byte("* err:" + err.Error()))
		return
	}
	log.Println(remoteStr, "Client name set:", client.Name)

	room.Join(&client)
	rdr := bufio.NewReader(client.Conn)
	for {
		buf, err := rdr.ReadBytes('\n')
		if err != nil {
			log.Println(remoteStr, "Error while reading connection:", err)
			break
		}
		buf = bytes.TrimRight(buf, "\r\n")
		room.BroadcastClientMessage(&client, string(buf))
	}
	room.Leave(&client)
}

func (r *Room) Join(c *Client) error {
	if _, exists := r.Clients[c.Name]; exists {
		return fmt.Errorf("client with the same name already exists: %s", c.Name)
	}
	r.BroadcastSystemMessage(c.Name + " has joined the room")
	r.Clients[c.Name] = c
	if err := r.ListClientsToClient(c); err != nil {
		return err
	}
	return nil
}

func (r *Room) ListClientsToClient(c *Client) error {
	var clientNames []string
	for _, client := range r.Clients {
		if client.Name != c.Name {
			clientNames = append(clientNames, client.Name)
		}
	}
	clientListMsg := "* The room contains: " + strings.Join(clientNames, ", ") + "\n"

	log.Printf("Sending client list to newly joined client (%s): %s", c.Name, clientListMsg)
	_, err := c.Conn.Write([]byte(clientListMsg))
	if err != nil {
		return err
	}
	return nil
}

func (r *Room) Leave(c *Client) {
	delete(r.Clients, c.Name)
	r.BroadcastSystemMessage(c.Name + " has left the room")
}

func (r *Room) BroadcastClientMessage(c *Client, message string) {
	r.broadcastMessage(c.Name, message, false)
}

func (r *Room) BroadcastSystemMessage(message string) {
	r.broadcastMessage("SYSTEM", message, true)
}

func (r *Room) broadcastMessage(sender string, message string, isSystem bool) {
	var msg string
	if isSystem {
		msg = "* " + message
	} else {
		msg = "[" + sender + "] " + message
	}

	log.Printf("Sender (%s) broadcasting message: %s\n", sender, msg)
	msg += "\n"
	for _, c := range r.Clients {
		if isSystem || sender != c.Name {
			log.Printf("Sending message from (%s) to (%s) system=%t\n", sender, c.Name, isSystem)
			go func(client *Client) {
				if _, err := client.Conn.Write([]byte(msg)); err != nil {
					log.Printf("Error while sending message to client (%s): %v\n", c.Name, err)
				}
			}(c)
		}
	}
}

func (c *Client) PromptName() error {
	remoteStr := "[" + c.Conn.RemoteAddr().String() + "]"
	log.Println(remoteStr, "Prompting client for a name")

	_, err := c.Conn.Write([]byte("Welcome to budget_chat! What should we call you?\n"))
	if err != nil {
		return err
	}

	rdr := bufio.NewReader(c.Conn)
	buf, err := rdr.ReadBytes('\n')
	if err != nil {
		return err
	}
	buf = bytes.TrimSpace(buf)

	if len(buf) == 0 {
		return fmt.Errorf("client sent empty name buffer")
	}
	invalidNameRegexp := regexp.MustCompile(`[^a-zA-Z0-9]`)
	if invalidNameRegexp.Match(buf) {
		return fmt.Errorf("invalid name received: %s", buf)
	}

	c.Name = string(buf)
	return nil
}
