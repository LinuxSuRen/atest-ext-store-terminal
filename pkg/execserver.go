package pkg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/creack/pty"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Terminal struct {
	TerminalId   string `json:"terminalId"`
	TerminalName string `json:"terminalName"`
	WSPort       int    `json:"wsPort"`
	Mode         string `json:"mode"`
}

type execRequest struct {
	Cmd string `json:"cmd"`
	Terminal
}

type inputRequest struct {
	Input string `json:"input"`
}

type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// WebSocket message types
type WSMessage struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Pid      int    `json:"pid,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ProcessManager manages running processes
type ProcessManager struct {
	processes map[int]*ProcessInfo
	mutex     sync.RWMutex
}

// ProcessInfo holds information about a running process
type ProcessInfo struct {
	Cmd    *exec.Cmd
	Stdin  *bufio.Writer
	Stdout *bufio.Reader
	Stderr *bufio.Reader
}

// Global process manager
var processManager = &ProcessManager{
	processes: make(map[int]*ProcessInfo),
}

type TerminalCache struct {
	Writer         io.WriteCloser
	Context        context.Context
	ResponseWriter http.ResponseWriter
	DoneChannel    chan bool
	Terminal
}

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow connections from any origin in this example
	},
}

var serverPort int

func SetServerPort(port int) {
	serverPort = port
}

// StartExecServer starts a small HTTP server to execute shell commands.
// It runs in a goroutine and allows cross-origin requests (for local dev).
func StartExecServer(addr string) net.Listener {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/exec", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req execRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Use shell to run the command so complex commands work.
		cmd := createCommand(ctx, req.Cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		resp := execResponse{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}
		if err != nil {
			resp.Error = err.Error()
			if exitErr, ok := err.(*exec.ExitError); ok {
				resp.ExitCode = exitErr.ExitCode()
			} else {
				resp.ExitCode = -1
			}
		} else {
			resp.ExitCode = 0
		}

		_ = json.NewEncoder(w).Encode(resp)
	})

	// WebSocket endpoint for command execution
	mux.HandleFunc("/extensionProxy/terminal/ws", handleWebSocket)

	cmdWriterCache := map[string]TerminalCache{}

	// Add streaming endpoint
	mux.HandleFunc("/extensionProxy/terminal/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}

		var req execRequest

		if r.Method == http.MethodDelete {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fmt.Println("terminating terminal", req.TerminalId)
			if c, ok := cmdWriterCache[req.TerminalId]; ok {
				c.Writer.Close()
				c.Context.Done()
				delete(cmdWriterCache, req.TerminalId)
			}
			return
		} else if r.Method == http.MethodGet {
			// get the keys of the map cmdWriterCache
			keys := make([]Terminal, 0, len(cmdWriterCache))
			for _, c := range cmdWriterCache {
				keys = append(keys, Terminal{
					TerminalId:   c.TerminalId,
					TerminalName: c.TerminalName,
					Mode:         runtime.GOOS,
					WSPort:       serverPort,
				})
			}
			if len(keys) == 0 {
				keys = []Terminal{
					{
						TerminalId:   "default",
						TerminalName: "Default",
						WSPort:       serverPort,
						Mode:         runtime.GOOS,
					},
				}
			}
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(keys)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		} else if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Accel-Buffering", "no") // Disable buffering for nginx
		if c, ok := cmdWriterCache[req.TerminalId]; ok {
			fmt.Println("sending command to existing terminal", req.TerminalId, "cmd:", req.Cmd)
			c.ResponseWriter = w
			_, err := c.Writer.Write([]byte(req.Cmd + "\n"))
			if err == nil {
				return
			} else {
				fmt.Println("failed to write to terminal", req.TerminalId, "cmd:", req.Cmd, "error:", err)
				go c.Context.Done()
				c.DoneChannel <- true
				delete(cmdWriterCache, req.TerminalId)
			}
		}

		// Create context with timeout
		ctx, cancel := context.WithCancel(context.Background()) // No timeout for interactive commands
		defer cancel()

		// Use shell to run the command so complex commands work.
		// For interactive commands like SSH, we need to allocate a pseudo-TTY
		cmd := createCommand(ctx, req.Cmd)

		// Check if this is an interactive command that needs a TTY
		if isInteractiveCommand(req.Cmd) {
			// Set environment variables to force TTY allocation
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
		}

		// Create stdin pipe to allow writing to the command
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			http.Error(w, "failed to create stdin pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Create pipes for stdout and stderr
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			http.Error(w, "failed to create stdout pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}

		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			http.Error(w, "failed to create stderr pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			http.Error(w, "failed to start command: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Add process to manager
		processInfo := &ProcessInfo{
			Cmd:   cmd,
			Stdin: bufio.NewWriter(stdinPipe),
		}
		processManager.mutex.Lock()
		processManager.processes[cmd.Process.Pid] = processInfo
		processManager.mutex.Unlock()

		// Send initial message
		fmt.Fprintf(w, "data: {\"type\": \"start\", \"pid\": %d}\n\n", cmd.Process.Pid)
		w.(http.Flusher).Flush()

		// Create scanners for stdout and stderr
		stdoutScanner := bufio.NewScanner(stdoutPipe)
		stderrScanner := bufio.NewScanner(stderrPipe)

		// Channels for output
		stdoutCh := make(chan string)
		stderrCh := make(chan string)
		doneCh := make(chan bool)

		cmdWriterCache[req.TerminalId] = TerminalCache{
			Writer:      stdinPipe,
			Context:     ctx,
			DoneChannel: doneCh,
			Terminal: Terminal{
				TerminalId:   req.TerminalId,
				TerminalName: req.TerminalName,
			},
		}

		// Variables to store exit code and error message
		var exitCode int
		var errorMsg string

		// Goroutine for stdout
		go func() {
			for stdoutScanner.Scan() {
				stdoutCh <- stdoutScanner.Text()
			}
			close(stdoutCh)
		}()

		// Goroutine for stderr
		go func() {
			for stderrScanner.Scan() {
				stderrCh <- stderrScanner.Text()
			}
			close(stderrCh)
		}()

		// Goroutine to wait for command completion
		go func() {
			err := cmd.Wait()

			exitCode = 0
			if err != nil {
				errorMsg = err.Error()
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}

			// Remove process from manager
			processManager.mutex.Lock()
			delete(processManager.processes, cmd.Process.Pid)
			processManager.mutex.Unlock()

			doneCh <- true
		}()

		// Main loop to handle output and input
		loop := true
		for loop {
			select {
			case stdoutLine, ok := <-stdoutCh:
				if ok {
					_, e := fmt.Fprintf(w, "data: {\"type\": \"stdout\", \"data\": %q}\n\n", stdoutLine)
					if e != nil {
						fmt.Println("failed to write to terminal", req.TerminalId, "stdout:", e)
					}
					w.(http.Flusher).Flush()
				}
			case stderrLine, ok := <-stderrCh:
				if ok {
					fmt.Fprintf(w, "data: {\"type\": \"stderr\", \"data\": %q}\n\n", stderrLine)
					w.(http.Flusher).Flush()
				}
				break
			case <-doneCh:
				// Command has finished executing, send final end event
				fmt.Fprintf(w, "data: {\"type\": \"end\", \"exitCode\": %d, \"error\": %q}\n\n", exitCode, errorMsg)
				w.(http.Flusher).Flush()
				// Close stdin pipe
				stdinPipe.Close()
				loop = false
			case <-ctx.Done():
				// Context cancelled, kill the process
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				fmt.Fprintf(w, "data: {\"type\": \"error\", \"data\": \"Command cancelled\"}\n\n")
				w.(http.Flusher).Flush()
				// Close stdin pipe
				stdinPipe.Close()
				// Remove process from manager
				processManager.mutex.Lock()
				delete(processManager.processes, cmd.Process.Pid)
				processManager.mutex.Unlock()
				loop = false
			}
		}
		delete(cmdWriterCache, req.TerminalId)
		fmt.Println("command finished", req.TerminalId, "exitCode:", exitCode, "error:", errorMsg)
	})

	// Add endpoint for sending input to running process
	mux.HandleFunc("/api/exec/input", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Pid   int    `json:"pid"`
			Input string `json:"input"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Find the process
		processManager.mutex.RLock()
		processInfo, exists := processManager.processes[req.Pid]
		processManager.mutex.RUnlock()

		if !exists {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}

		// Write input to process stdin
		_, err := processInfo.Stdin.WriteString(req.Input)
		if err != nil {
			http.Error(w, "failed to write to process stdin: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Flush the buffer
		err = processInfo.Stdin.Flush()
		if err != nil {
			http.Error(w, "failed to flush stdin buffer: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "success"})
	})

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	go func() {
		if err := http.Serve(lis, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("exec server error: %v", err)
		}
	}()
	return lis
}

// handleWebSocket handles WebSocket connections for command execution
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	shell := os.Getenv("SHELL")
	if shell == "" {
		switch runtime.GOOS {
		case "windows":
			shell = "powershell.exe"
		default:
			shell = "/bin/sh"
		}
	}
	cmd := exec.Command(shell)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("pty start shell: %s, err: %v", shell, err)
		return
	}
	defer func() { _ = ptmx.Close(); cmd.Process.Kill() }()

	var wg sync.WaitGroup
	wg.Add(2)

	// 2. WebSocket → pty
	go func() {
		defer wg.Done()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if _, err := ptmx.Write(msg); err != nil {
				return
			}
		}
	}()

	// 3. pty → WebSocket
	go func() {
		defer wg.Done()
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// 4. 阻塞直到任一端断开
	wg.Wait()
}

// executeCommandViaWS executes a command and streams output via WebSocket
func executeCommandViaWS(conn *websocket.Conn, req execRequest) {
	// Send start message
	sendWSMessage(conn, WSMessage{
		Type: "start",
	})

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use shell to run the command so complex commands work.
	cmd := createCommand(ctx, req.Cmd)

	// Check if this is an interactive command that needs a TTY
	if isInteractiveCommand(req.Cmd) {
		// Set environment variables to force TTY allocation
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	}

	// Create pipes for stdout and stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		sendWSMessage(conn, WSMessage{
			Type:  "error",
			Error: "Failed to create stdout pipe: " + err.Error(),
		})
		return
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		sendWSMessage(conn, WSMessage{
			Type:  "error",
			Error: "Failed to create stderr pipe: " + err.Error(),
		})
		return
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		sendWSMessage(conn, WSMessage{
			Type:  "error",
			Error: "Failed to start command: " + err.Error(),
		})
		return
	}

	// Send PID
	sendWSMessage(conn, WSMessage{
		Type: "pid",
		Pid:  cmd.Process.Pid,
	})

	// Create scanners for stdout and stderr
	stdoutScanner := bufio.NewScanner(stdoutPipe)
	stderrScanner := bufio.NewScanner(stderrPipe)

	// Channels for output
	stdoutCh := make(chan string)
	stderrCh := make(chan string)
	doneCh := make(chan int) // Channel to signal completion with exit code

	// Goroutine for stdout
	go func() {
		for stdoutScanner.Scan() {
			stdoutCh <- stdoutScanner.Text()
		}
		close(stdoutCh)
	}()

	// Goroutine for stderr
	go func() {
		for stderrScanner.Scan() {
			stderrCh <- stderrScanner.Text()
		}
		close(stderrCh)
	}()

	// Goroutine to wait for command completion
	go func() {
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		doneCh <- exitCode
	}()

	// Main loop to handle output
	for {
		select {
		case stdoutLine, ok := <-stdoutCh:
			if ok {
				sendWSMessage(conn, WSMessage{
					Type: "stdout",
					Data: stdoutLine,
				})
			}
		case stderrLine, ok := <-stderrCh:
			if ok {
				sendWSMessage(conn, WSMessage{
					Type: "stderr",
					Data: stderrLine,
				})
			}
		case exitCode := <-doneCh:
			// Command has finished executing
			sendWSMessage(conn, WSMessage{
				Type:     "end",
				ExitCode: exitCode,
			})
			return
		}
	}
}

// sendWSMessage sends a message via WebSocket
func sendWSMessage(conn *websocket.Conn, msg WSMessage) error {
	return conn.WriteJSON(msg)
}

// isInteractiveCommand checks if a command is likely to be interactive
func isInteractiveCommand(cmd string) bool {
	interactiveCommands := []string{"ssh", "telnet", "mysql", "psql", "mongo", "redis-cli"}
	for _, interactiveCmd := range interactiveCommands {
		if len(cmd) >= len(interactiveCmd) && cmd[:len(interactiveCmd)] == interactiveCmd {
			return true
		}
	}
	return false
}

// createCommand creates an exec.Command based on the operating system
func createCommand(ctx context.Context, cmdString string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd.exe", "/c", cmdString)
	}
	return exec.CommandContext(ctx, "sh", "-c", cmdString)
}
