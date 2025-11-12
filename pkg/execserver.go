package pkg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

type execRequest struct {
	Cmd        string `json:"cmd"`
	TerminalId string `json:"terminalId"`
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

	cmdWriterCache := map[string]io.Writer{}

	// Add streaming endpoint
	mux.HandleFunc("/extensionProxy/terminal", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Accel-Buffering", "no") // Disable buffering for nginx

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

		if c, ok := cmdWriterCache[req.TerminalId]; ok {
			fmt.Println("sending command to existing terminal", req.TerminalId, "cmd:", req.Cmd)
			_, err := c.Write([]byte(req.Cmd + "\n"))
			if err == nil {
				return
			}
			cmdWriterCache[req.TerminalId] = nil
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
		cmdWriterCache[req.TerminalId] = stdinPipe

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
		for {
			select {
			case stdoutLine, ok := <-stdoutCh:
				if ok {
					fmt.Fprintf(w, "data: {\"type\": \"stdout\", \"data\": %q}\n\n", stdoutLine)
					w.(http.Flusher).Flush()
				}
			case stderrLine, ok := <-stderrCh:
				if ok {
					fmt.Fprintf(w, "data: {\"type\": \"stderr\", \"data\": %q}\n\n", stderrLine)
					w.(http.Flusher).Flush()
				}
			case <-doneCh:
				// Command has finished executing, send final end event
				fmt.Fprintf(w, "data: {\"type\": \"end\", \"exitCode\": %d, \"error\": %q}\n\n", exitCode, errorMsg)
				w.(http.Flusher).Flush()
				// Close stdin pipe
				stdinPipe.Close()
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
			}
		}
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
