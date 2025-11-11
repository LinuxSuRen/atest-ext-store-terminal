package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"time"
)

type execRequest struct {
	Cmd string `json:"cmd"`
}

type execResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// StartExecServer starts a small HTTP server to execute shell commands.
// It runs in a goroutine and allows cross-origin requests (for local dev).
func StartExecServer(addr string) {
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
		cmd := exec.CommandContext(ctx, "sh", "-c", req.Cmd)
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

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("exec server error: %v", err)
		}
	}()
}
