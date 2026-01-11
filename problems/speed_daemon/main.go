package main

import (
	"encoding/binary"
	"io"
	"log"
	"math"
	"net"
	"os"
	"sync"
	"time"
)

type Road uint16
type Plate string
type Day uint32
type Speed uint16 // 100x mph
type Mile uint16
type Timestamp uint32
type Limit uint16 // mph

type ClientType int

const (
	ClientUnknown ClientType = iota
	ClientCamera
	ClientDispatcher
)

type Camera struct {
	Road  Road
	Mile  Mile
	Limit Limit
}

type Dispatcher struct {
	Roads map[Road]bool
}

type HeartbeatConfig struct {
	Interval uint32 // deciseconds
	LastSent time.Time
	stopCh   chan struct{}
}

type Client struct {
	Conn       net.Conn
	Type       ClientType
	Camera     *Camera
	Dispatcher *Dispatcher
	Heartbeat  *HeartbeatConfig
	Identified bool
	mu         sync.Mutex
}

type Observation struct {
	Plate     Plate
	Timestamp Timestamp
	Mile      Mile
}

type Ticket struct {
	Plate      Plate
	Road       Road
	Mile1      Mile
	Timestamp1 Timestamp
	Mile2      Mile
	Timestamp2 Timestamp
	Speed      Speed
}

// Global state with mutex protection
var (
	clientsMu         sync.RWMutex
	clients           = make(map[*Client]bool)
	observationsMu    sync.RWMutex
	observations      = make(map[Road]map[Plate][]Observation)
	ticketQueueMu     sync.RWMutex
	ticketQueue       = make(map[Road][]Ticket)
	ticketedDaysMu    sync.RWMutex
	ticketedDays      = make(map[Plate]map[Day]bool)
	dispatchersMu     sync.RWMutex
	dispatchersByRoad = make(map[Road][]*Client)
)

func readU8(conn net.Conn) (uint8, error) {
	buf := make([]byte, 1)
	_, err := io.ReadFull(conn, buf)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

func readU16(conn net.Conn) (uint16, error) {
	buf := make([]byte, 2)
	_, err := io.ReadFull(conn, buf)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(buf), nil
}

func readU32(conn net.Conn) (uint32, error) {
	buf := make([]byte, 4)
	_, err := io.ReadFull(conn, buf)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf), nil
}

func readString(conn net.Conn) (string, error) {
	length, err := readU8(conn)
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	buf := make([]byte, length)
	_, err = io.ReadFull(conn, buf)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeU8(conn net.Conn, val uint8) error {
	_, err := conn.Write([]byte{val})
	return err
}

func writeU16(conn net.Conn, val uint16) error {
	buf := make([]byte, 2)
	binary.BigEndian.PutUint16(buf, val)
	_, err := conn.Write(buf)
	return err
}

func writeU32(conn net.Conn, val uint32) error {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, val)
	_, err := conn.Write(buf)
	return err
}

func writeString(conn net.Conn, str string) error {
	length := uint8(len(str))
	if err := writeU8(conn, length); err != nil {
		return err
	}
	if length > 0 {
		_, err := conn.Write([]byte(str))
		return err
	}
	return nil
}

func sendError(conn net.Conn, msg string) error {
	if err := writeU8(conn, 0x10); err != nil {
		return err
	}
	return writeString(conn, msg)
}

func calculateSpeed(mile1, mile2 Mile, timestamp1, timestamp2 Timestamp) Speed {
	distance := int32(mile2) - int32(mile1)
	if distance < 0 {
		distance = -distance
	}
	timeDiff := int64(timestamp2) - int64(timestamp1)

	if timeDiff <= 0 {
		return 0
	}

	// speed = (distance / time) * 3600 (to convert to mph if timestamps are seconds)
	speedMph := float64(distance) / float64(timeDiff) * 3600.0
	// Round to nearest 0.01 mph for 100x representation
	return Speed(math.Round(speedMph * 100))
}

func getDay(timestamp Timestamp) Day {
	return Day(timestamp / 86400)
}

func isTicketedForDays(plate Plate, day1, day2 Day) bool {
	ticketedDaysMu.RLock()
	defer ticketedDaysMu.RUnlock()

	days, exists := ticketedDays[plate]
	if !exists {
		return false
	}

	for day := day1; day <= day2; day++ {
		if days[day] {
			return true
		}
	}
	return false
}

func markDaysAsTicketed(plate Plate, day1, day2 Day) {
	ticketedDaysMu.Lock()
	defer ticketedDaysMu.Unlock()

	if ticketedDays[plate] == nil {
		ticketedDays[plate] = make(map[Day]bool)
	}

	for day := day1; day <= day2; day++ {
		ticketedDays[plate][day] = true
	}
}

func storeObservationAndCheckViolations(road Road, obs Observation, limit Limit) {
	observationsMu.Lock()
	if observations[road] == nil {
		observations[road] = make(map[Plate][]Observation)
	}
	if observations[road][obs.Plate] == nil {
		observations[road][obs.Plate] = make([]Observation, 0)
	}
	observations[road][obs.Plate] = append(observations[road][obs.Plate], obs)
	existingObs := make([]Observation, len(observations[road][obs.Plate])-1)
	copy(existingObs, observations[road][obs.Plate][:len(observations[road][obs.Plate])-1])
	observationsMu.Unlock()

	// Check for violations between the new observation and all existing observations
	// This ensures each pair is only checked once (when the second observation arrives)
	// and we catch violations even if observations arrive out of order
	for _, existing := range existingObs {
		obs1 := obs
		obs2 := existing
		var mile1, mile2 Mile
		var timestamp1, timestamp2 Timestamp

		if obs1.Timestamp < obs2.Timestamp {
			mile1 = obs1.Mile
			timestamp1 = obs1.Timestamp
			mile2 = obs2.Mile
			timestamp2 = obs2.Timestamp
		} else {
			mile1 = obs2.Mile
			timestamp1 = obs2.Timestamp
			mile2 = obs1.Mile
			timestamp2 = obs1.Timestamp
		}

		speed := calculateSpeed(mile1, mile2, timestamp1, timestamp2)

		if speed == 0 {
			continue
		}

		// Check if speed exceeds limit by 0.5 mph or more
		// speed is in 100x mph, limit is in mph
		// We ticket if speed >= (limit + 0.5) * 100
		// Which is: speed >= limit * 100 + 50
		limitIn100x := uint16(limit) * 100
		if uint16(speed) < limitIn100x+50 {
			continue
		}

		day1 := getDay(timestamp1)
		day2 := getDay(timestamp2)

		ticketedDaysMu.Lock()
		alreadyTicketed := false
		if ticketedDays[obs1.Plate] != nil {
			for day := day1; day <= day2; day++ {
				if ticketedDays[obs1.Plate][day] {
					alreadyTicketed = true
					break
				}
			}
		}

		if alreadyTicketed {
			ticketedDaysMu.Unlock()
			continue
		}

		if ticketedDays[obs1.Plate] == nil {
			ticketedDays[obs1.Plate] = make(map[Day]bool)
		}
		for day := day1; day <= day2; day++ {
			ticketedDays[obs1.Plate][day] = true
		}
		ticketedDaysMu.Unlock()

		ticket := Ticket{
			Plate:      obs1.Plate,
			Road:       road,
			Mile1:      mile1,
			Timestamp1: timestamp1,
			Mile2:      mile2,
			Timestamp2: timestamp2,
			Speed:      speed,
		}

		deliverTicket(ticket)
	}
}

func deliverTicket(ticket Ticket) {
	dispatchersMu.RLock()
	dispatchers, exists := dispatchersByRoad[ticket.Road]
	dispatchersMu.RUnlock()

	if !exists || len(dispatchers) == 0 {
		ticketQueueMu.Lock()
		ticketQueue[ticket.Road] = append(ticketQueue[ticket.Road], ticket)
		ticketQueueMu.Unlock()
		return
	}

	validDispatchers := make([]*Client, 0)
	for _, d := range dispatchers {
		d.mu.Lock()
		dispatcher := d
		dispatcherType := d.Type
		dispatcherRoads := d.Dispatcher
		d.mu.Unlock()

		if dispatcherType == ClientDispatcher && dispatcherRoads != nil {
			if dispatcherRoads.Roads[ticket.Road] {
				validDispatchers = append(validDispatchers, dispatcher)
			}
		}
	}

	if len(validDispatchers) == 0 {
		ticketQueueMu.Lock()
		ticketQueue[ticket.Road] = append(ticketQueue[ticket.Road], ticket)
		ticketQueueMu.Unlock()
		return
	}

	dispatcher := validDispatchers[0]

	if err := sendTicket(dispatcher.Conn, ticket); err != nil {
		log.Printf("[%s] Error sending ticket to dispatcher: %v", dispatcher.Conn.RemoteAddr(), err)
	}
}

func sendTicket(conn net.Conn, ticket Ticket) error {
	if err := writeU8(conn, 0x21); err != nil {
		return err
	}
	if err := writeString(conn, string(ticket.Plate)); err != nil {
		return err
	}
	if err := writeU16(conn, uint16(ticket.Road)); err != nil {
		return err
	}
	if err := writeU16(conn, uint16(ticket.Mile1)); err != nil {
		return err
	}
	if err := writeU32(conn, uint32(ticket.Timestamp1)); err != nil {
		return err
	}
	if err := writeU16(conn, uint16(ticket.Mile2)); err != nil {
		return err
	}
	if err := writeU32(conn, uint32(ticket.Timestamp2)); err != nil {
		return err
	}
	if err := writeU16(conn, uint16(ticket.Speed)); err != nil {
		return err
	}
	return nil
}

func deliverQueuedTicketsForRoad(road Road, dispatcher *Client) {
	ticketQueueMu.Lock()
	tickets, exists := ticketQueue[road]
	if exists && len(tickets) > 0 {
		ticketQueue[road] = make([]Ticket, 0)
	}
	ticketQueueMu.Unlock()

	if !exists || len(tickets) == 0 {
		return
	}

	for _, ticket := range tickets {
		if err := sendTicket(dispatcher.Conn, ticket); err != nil {
			log.Printf("[%s] Error sending queued ticket: %v", dispatcher.Conn.RemoteAddr(), err)
			return
		}
	}
}

func startHeartbeat(client *Client) {
	if client.Heartbeat == nil || client.Heartbeat.Interval == 0 {
		return
	}

	client.Heartbeat.stopCh = make(chan struct{})
	interval := time.Duration(client.Heartbeat.Interval) * 100 * time.Millisecond

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				client.mu.Lock()
				if client.Heartbeat == nil || client.Conn == nil {
					client.mu.Unlock()
					return
				}
				conn := client.Conn
				client.mu.Unlock()

				if err := writeU8(conn, 0x41); err != nil {
					log.Printf("[%s] Error sending heartbeat: %v", conn.RemoteAddr(), err)
					return
				}
			case <-client.Heartbeat.stopCh:
				return
			}
		}
	}()
}

func stopHeartbeat(client *Client) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.Heartbeat != nil && client.Heartbeat.stopCh != nil {
		close(client.Heartbeat.stopCh)
	}
}

func removeDispatcher(client *Client) {
	dispatchersMu.Lock()
	defer dispatchersMu.Unlock()

	if client.Dispatcher == nil {
		return
	}

	roadList := make([]Road, 0, len(client.Dispatcher.Roads))
	for road := range client.Dispatcher.Roads {
		dispatchers, exists := dispatchersByRoad[road]
		if !exists {
			continue
		}

		for i, d := range dispatchers {
			if d == client {
				if len(dispatchers) == 1 {
					delete(dispatchersByRoad, road)
				} else {
					dispatchersByRoad[road] = append(dispatchers[:i], dispatchers[i+1:]...)
				}
				roadList = append(roadList, road)
				break
			}
		}
	}
}

func handleIAmCamera(client *Client, conn net.Conn) error {
	client.mu.Lock()
	if client.Identified {
		client.mu.Unlock()
		sendError(conn, "Already identified")
		return io.EOF
	}
	client.mu.Unlock()

	road, err := readU16(conn)
	if err != nil {
		return err
	}
	mile, err := readU16(conn)
	if err != nil {
		return err
	}
	limit, err := readU16(conn)
	if err != nil {
		return err
	}

	client.mu.Lock()
	client.Type = ClientCamera
	client.Camera = &Camera{
		Road:  Road(road),
		Mile:  Mile(mile),
		Limit: Limit(limit),
	}
	client.Identified = true
	client.mu.Unlock()

	return nil
}

func handleIAmDispatcher(client *Client, conn net.Conn) error {
	client.mu.Lock()
	if client.Identified {
		client.mu.Unlock()
		sendError(conn, "Already identified")
		return io.EOF
	}
	client.mu.Unlock()

	numroads, err := readU8(conn)
	if err != nil {
		return err
	}

	roads := make(map[Road]bool)
	for i := uint8(0); i < numroads; i++ {
		road, err := readU16(conn)
		if err != nil {
			return err
		}
		roads[Road(road)] = true
	}

	client.mu.Lock()
	client.Type = ClientDispatcher
	client.Dispatcher = &Dispatcher{Roads: roads}
	client.Identified = true
	client.mu.Unlock()

	dispatchersMu.Lock()
	roadList := make([]Road, 0, len(roads))
	for road := range roads {
		dispatchersByRoad[road] = append(dispatchersByRoad[road], client)
		roadList = append(roadList, road)
	}
	dispatchersMu.Unlock()

	for road := range roads {
		deliverQueuedTicketsForRoad(road, client)
	}

	return nil
}

func handlePlate(client *Client, conn net.Conn) error {
	client.mu.Lock()
	if client.Type != ClientCamera {
		client.mu.Unlock()
		sendError(conn, "Not a camera")
		return io.EOF
	}
	camera := client.Camera
	client.mu.Unlock()

	plateStr, err := readString(conn)
	if err != nil {
		return err
	}
	timestamp, err := readU32(conn)
	if err != nil {
		return err
	}

	obs := Observation{
		Plate:     Plate(plateStr),
		Timestamp: Timestamp(timestamp),
		Mile:      camera.Mile,
	}

	storeObservationAndCheckViolations(camera.Road, obs, camera.Limit)
	return nil
}

func handleWantHeartbeat(client *Client, conn net.Conn) error {
	client.mu.Lock()
	if client.Heartbeat != nil {
		client.mu.Unlock()
		sendError(conn, "Heartbeat already requested")
		return io.EOF
	}
	client.mu.Unlock()

	interval, err := readU32(conn)
	if err != nil {
		return err
	}

	client.mu.Lock()
	client.Heartbeat = &HeartbeatConfig{
		Interval: interval,
	}
	client.mu.Unlock()

	if interval > 0 {
		startHeartbeat(client)
	}

	return nil
}

func handleConn(conn net.Conn) {
	remoteStr := "[" + conn.RemoteAddr().String() + "]"
	defer func() {
		log.Println(remoteStr, "Closing connection")
		conn.Close()
	}()

	log.Println(remoteStr, "Accepted connection")

	client := &Client{
		Conn:       conn,
		Type:       ClientUnknown,
		Identified: false,
	}

	clientsMu.Lock()
	clients[client] = true
	clientsMu.Unlock()

	defer func() {
		clientsMu.Lock()
		delete(clients, client)
		clientsMu.Unlock()

		stopHeartbeat(client)
		removeDispatcher(client)
	}()

	for {
		msgType, err := readU8(conn)
		if err != nil {
			if err != io.EOF {
				log.Println(remoteStr, "Error reading message type:", err)
			}
			return
		}

		switch msgType {
		case 0x80:
			if err := handleIAmCamera(client, conn); err != nil {
				if err == io.EOF {
					return
				}
				log.Println(remoteStr, "Error handling IAmCamera:", err)
				return
			}
		case 0x81:
			if err := handleIAmDispatcher(client, conn); err != nil {
				if err == io.EOF {
					return
				}
				log.Println(remoteStr, "Error handling IAmDispatcher:", err)
				return
			}
		case 0x20:
			if err := handlePlate(client, conn); err != nil {
				if err == io.EOF {
					return
				}
				log.Println(remoteStr, "Error handling Plate:", err)
				sendError(conn, "Protocol error")
				return
			}
		case 0x40:
			if err := handleWantHeartbeat(client, conn); err != nil {
				if err == io.EOF {
					return
				}
				log.Println(remoteStr, "Error handling WantHeartbeat:", err)
				sendError(conn, "Protocol error")
				return
			}
		default:
			log.Println(remoteStr, "Unknown message type:", msgType)
			sendError(conn, "Unknown message type")
			return
		}
	}
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
