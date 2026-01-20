package main

import (
	"bufio"
	"container/heap"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

type Job struct {
	ID       int64
	Queue    string
	Priority int
	JobData  json.RawMessage
}

type PriorityQueue []*Job

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// Max-heap: higher priority first; for same priority, lower ID first (FIFO-like)
	if pq[i].Priority == pq[j].Priority {
		return pq[i].ID < pq[j].ID
	}
	return pq[i].Priority > pq[j].Priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *PriorityQueue) Push(x interface{}) {
	*pq = append(*pq, x.(*Job))
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

type Client struct {
	Conn         net.Conn
	WorkingJobs  map[int64]bool
	WaitChannels []chan *Job
	mu           sync.Mutex
}

type Request struct {
	Request string          `json:"request"`
	Queue   string          `json:"queue,omitempty"`
	Queues  []string        `json:"queues,omitempty"`
	Job     json.RawMessage `json:"job,omitempty"`
	Pri     *int            `json:"pri,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Wait    *bool           `json:"wait,omitempty"`
}

type Response struct {
	Status string          `json:"status"`
	ID     *int64          `json:"id,omitempty"`
	Job    json.RawMessage `json:"job,omitempty"`
	Pri    *int            `json:"pri,omitempty"`
	Queue  string          `json:"queue,omitempty"`
	Error  string          `json:"error,omitempty"`
}

var (
	jobIDCounter int64

	// Queue management
	queuesMu sync.RWMutex
	queues   = make(map[string]*PriorityQueue)

	// Job lookup by ID
	jobsMu sync.RWMutex
	jobs   = make(map[int64]*Job)

	// Client tracking
	clientsMu sync.RWMutex
	clients   = make(map[*Client]bool)

	// Job ownership: which client is working on which job
	jobOwnersMu sync.RWMutex
	jobOwners   = make(map[int64]*Client)

	// Waiting clients: channels for blocking get requests
	waitersMu sync.Mutex
	waiters   = make(map[string][]chan *Job)
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

func nextJobID() int64 {
	return atomic.AddInt64(&jobIDCounter, 1)
}

func getOrCreateQueue(queueName string) *PriorityQueue {
	queuesMu.RLock()
	pq, exists := queues[queueName]
	queuesMu.RUnlock()

	if exists {
		return pq
	}

	queuesMu.Lock()
	defer queuesMu.Unlock()

	// Double-check pattern: another goroutine may have created the queue
	if existingPq, exists := queues[queueName]; exists {
		return existingPq
	}

	pq = &PriorityQueue{}
	heap.Init(pq)
	queues[queueName] = pq
	return pq
}

func sendResponse(conn net.Conn, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}

func sendError(conn net.Conn, msg string) error {
	return sendResponse(conn, Response{
		Status: "error",
		Error:  msg,
	})
}

func handlePut(client *Client, req Request) error {
	if req.Queue == "" {
		return sendError(client.Conn, "Missing queue field")
	}
	if req.Pri == nil {
		return sendError(client.Conn, "Missing pri field")
	}
	if *req.Pri < 0 {
		return sendError(client.Conn, "Invalid priority")
	}
	if req.Job == nil {
		return sendError(client.Conn, "Missing job field")
	}

	jobID := nextJobID()
	job := &Job{
		ID:       jobID,
		Queue:    req.Queue,
		Priority: *req.Pri,
		JobData:  req.Job,
	}

	jobsMu.Lock()
	jobs[jobID] = job
	jobsMu.Unlock()

	// If there are waiting clients, send job directly to avoid queue overhead
	waitersMu.Lock()
	waitersList, hasWaiters := waiters[req.Queue]
	hasWaiters = hasWaiters && len(waitersList) > 0
	waitersMu.Unlock()

	if hasWaiters && notifyWaiters(req.Queue, job) {
		// Job sent to waiting client, skip queue
	} else {
		pq := getOrCreateQueue(req.Queue)
		queuesMu.Lock()
		heap.Push(pq, job)
		queuesMu.Unlock()
	}

	resp := Response{
		Status: "ok",
		ID:     &jobID,
	}
	return sendResponse(client.Conn, resp)
}

func notifyWaiters(queueName string, job *Job) bool {
	waitersMu.Lock()
	waitersList, exists := waiters[queueName]
	if !exists || len(waitersList) == 0 {
		waitersMu.Unlock()
		return false
	}

	ch := waitersList[0]
	waiters[queueName] = waitersList[1:]
	waitersMu.Unlock()

	// Non-blocking send with panic recovery for closed channels
	sent := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Channel closed (client disconnected)
			}
		}()
		select {
		case ch <- job:
			sent = true
		default:
		}
	}()
	return sent
}

func findHighestPriorityJob(queueNames []string) *Job {
	var bestJob *Job
	var bestQueue string

	// First pass: find best job with read lock
	queuesMu.RLock()
	for _, queueName := range queueNames {
		pq, exists := queues[queueName]
		if !exists || pq.Len() == 0 {
			continue
		}
		topJob := (*pq)[0]
		if bestJob == nil || topJob.Priority > bestJob.Priority ||
			(topJob.Priority == bestJob.Priority && topJob.ID < bestJob.ID) {
			bestJob = topJob
			bestQueue = queueName
		}
	}
	queuesMu.RUnlock()

	if bestJob == nil {
		return nil
	}

	// Second pass: remove job with write lock (re-check to handle race conditions)
	queuesMu.Lock()
	pq, exists := queues[bestQueue]
	if exists && pq.Len() > 0 && (*pq)[0].ID == bestJob.ID {
		heap.Pop(pq)
		queuesMu.Unlock()
		return bestJob
	}
	queuesMu.Unlock()

	// Job was removed by another goroutine, retry
	return findHighestPriorityJob(queueNames)
}

func handleGet(client *Client, req Request) error {
	if len(req.Queues) == 0 {
		return sendError(client.Conn, "Missing or empty queues field")
	}

	wait := req.Wait != nil && *req.Wait
	job := findHighestPriorityJob(req.Queues)

	if job == nil {
		if !wait {
			return sendResponse(client.Conn, Response{
				Status: "no-job",
			})
		}

		// Blocking wait: register channel and wait for job
		waitCh := make(chan *Job, 1)
		waitersMu.Lock()
		for _, queueName := range req.Queues {
			waiters[queueName] = append(waiters[queueName], waitCh)
		}
		waitersMu.Unlock()

		client.mu.Lock()
		client.WaitChannels = append(client.WaitChannels, waitCh)
		client.mu.Unlock()

		receivedJob, ok := <-waitCh
		if !ok {
			return nil // Channel closed (client disconnected)
		}
		job = receivedJob

		// Clean up waiters registration
		waitersMu.Lock()
		for _, queueName := range req.Queues {
			list := waiters[queueName]
			for i, ch := range list {
				if ch == waitCh {
					waiters[queueName] = append(list[:i], list[i+1:]...)
					break
				}
			}
		}
		waitersMu.Unlock()

		client.mu.Lock()
		for i, ch := range client.WaitChannels {
			if ch == waitCh {
				client.WaitChannels = append(client.WaitChannels[:i], client.WaitChannels[i+1:]...)
				break
			}
		}
		client.mu.Unlock()
	}

	if job == nil {
		return sendResponse(client.Conn, Response{
			Status: "no-job",
		})
	}

	// Track job ownership for abort/delete operations
	client.mu.Lock()
	if client.WorkingJobs == nil {
		client.WorkingJobs = make(map[int64]bool)
	}
	client.WorkingJobs[job.ID] = true
	client.mu.Unlock()

	jobOwnersMu.Lock()
	jobOwners[job.ID] = client
	jobOwnersMu.Unlock()

	resp := Response{
		Status: "ok",
		ID:     &job.ID,
		Job:    job.JobData,
		Pri:    &job.Priority,
		Queue:  job.Queue,
	}
	return sendResponse(client.Conn, resp)
}

func handleDelete(client *Client, req Request) error {
	if req.ID == nil {
		return sendError(client.Conn, "Missing id field")
	}

	jobID := *req.ID
	jobsMu.RLock()
	job, exists := jobs[jobID]
	jobsMu.RUnlock()

	if !exists {
		return sendResponse(client.Conn, Response{
			Status: "no-job",
		})
	}

	jobsMu.Lock()
	delete(jobs, jobID)
	jobsMu.Unlock()

	// Remove from queue if still present (may have been retrieved)
	queuesMu.Lock()
	pq, exists := queues[job.Queue]
	if exists {
		for i, j := range *pq {
			if j.ID == jobID {
				heap.Remove(pq, i)
				break
			}
		}
	}
	queuesMu.Unlock()

	jobOwnersMu.Lock()
	owner, wasOwned := jobOwners[jobID]
	delete(jobOwners, jobID)
	jobOwnersMu.Unlock()

	if wasOwned && owner != nil {
		owner.mu.Lock()
		delete(owner.WorkingJobs, jobID)
		owner.mu.Unlock()
	}

	return sendResponse(client.Conn, Response{
		Status: "ok",
	})
}

func handleAbort(client *Client, req Request) error {
	if req.ID == nil {
		return sendError(client.Conn, "Missing id field")
	}

	jobID := *req.ID
	jobOwnersMu.RLock()
	owner, exists := jobOwners[jobID]
	jobOwnersMu.RUnlock()

	if !exists || owner != client {
		jobsMu.RLock()
		_, jobExists := jobs[jobID]
		jobsMu.RUnlock()

		if !jobExists {
			return sendResponse(client.Conn, Response{
				Status: "no-job",
			})
		}
		return sendError(client.Conn, "Job not owned by this client")
	}

	jobsMu.RLock()
	job, exists := jobs[jobID]
	jobsMu.RUnlock()

	if !exists {
		return sendResponse(client.Conn, Response{
			Status: "no-job",
		})
	}

	jobOwnersMu.Lock()
	delete(jobOwners, jobID)
	jobOwnersMu.Unlock()

	client.mu.Lock()
	delete(client.WorkingJobs, jobID)
	client.mu.Unlock()

	// Return job to queue or send to waiting client
	waitersMu.Lock()
	waitersList, hasWaiters := waiters[job.Queue]
	hasWaiters = hasWaiters && len(waitersList) > 0
	waitersMu.Unlock()

	jobSent := hasWaiters && notifyWaiters(job.Queue, job)
	if !jobSent {
		pq := getOrCreateQueue(job.Queue)
		queuesMu.Lock()
		heap.Push(pq, job)
		queuesMu.Unlock()
	}

	return sendResponse(client.Conn, Response{
		Status: "ok",
	})
}

func abortClientJobs(client *Client) {
	client.mu.Lock()
	workingJobs := make([]int64, 0, len(client.WorkingJobs))
	for jobID := range client.WorkingJobs {
		workingJobs = append(workingJobs, jobID)
	}
	client.mu.Unlock()

	for _, jobID := range workingJobs {
		jobsMu.RLock()
		job, exists := jobs[jobID]
		jobsMu.RUnlock()

		if !exists {
			continue
		}

		jobOwnersMu.Lock()
		delete(jobOwners, jobID)
		jobOwnersMu.Unlock()

		// Return job to queue or send to waiting client
		waitersMu.Lock()
		waitersList, hasWaiters := waiters[job.Queue]
		hasWaiters = hasWaiters && len(waitersList) > 0
		waitersMu.Unlock()

		jobSent := hasWaiters && notifyWaiters(job.Queue, job)
		if !jobSent {
			pq := getOrCreateQueue(job.Queue)
			queuesMu.Lock()
			heap.Push(pq, job)
			queuesMu.Unlock()
		}
	}

	client.mu.Lock()
	client.WorkingJobs = make(map[int64]bool)
	client.mu.Unlock()
}

func handleConn(conn net.Conn) {
	remoteStr := "[" + conn.RemoteAddr().String() + "]"
	defer func() {
		log.Printf("%s Closing connection", remoteStr)
		conn.Close()
	}()

	log.Printf("%s Accepted connection", remoteStr)

	client := &Client{
		Conn:         conn,
		WorkingJobs:  make(map[int64]bool),
		WaitChannels: make([]chan *Job, 0),
	}

	clientsMu.Lock()
	clients[client] = true
	clientsMu.Unlock()

	defer func() {
		clientsMu.Lock()
		delete(clients, client)
		clientsMu.Unlock()

		// Clean up waiting channels and unblock waiting goroutines
		client.mu.Lock()
		waitChannels := client.WaitChannels
		client.WaitChannels = nil
		client.mu.Unlock()

		waitersMu.Lock()
		for _, waitCh := range waitChannels {
			// Remove from all queues (we don't know which queues client was waiting on)
			for queueName, list := range waiters {
				for i, ch := range list {
					if ch == waitCh {
						waiters[queueName] = append(list[:i], list[i+1:]...)
						break
					}
				}
			}
			close(waitCh)
		}
		waitersMu.Unlock()

		abortClientJobs(client)
	}()

	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				log.Println(remoteStr, "Error reading:", err)
			}
			return
		}

		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			sendError(conn, "Invalid JSON")
			continue
		}

		switch req.Request {
		case "put":
			if err := handlePut(client, req); err != nil {
				log.Println(remoteStr, "Error handling put:", err)
				return
			}
		case "get":
			if err := handleGet(client, req); err != nil {
				log.Println(remoteStr, "Error handling get:", err)
				return
			}
		case "delete":
			if err := handleDelete(client, req); err != nil {
				log.Println(remoteStr, "Error handling delete:", err)
				return
			}
		case "abort":
			if err := handleAbort(client, req); err != nil {
				log.Println(remoteStr, "Error handling abort:", err)
				return
			}
		default:
			sendError(conn, "Unrecognised request type.")
			continue
		}
	}
}
