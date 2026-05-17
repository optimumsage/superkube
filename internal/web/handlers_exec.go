package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// wsExec is the WebSocket handler for `kubectl exec -it <pod>`. The browser
// runs an xterm.js terminal and pipes user input as JSON frames; we pipe
// kubectl's pty output back as JSON frames. The flow:
//
//	client  ── JSON {type:"input"|"resize"} ──►  server
//	client  ◄── JSON {type:"output"|"exit"} ──   server
//
// Server-side, kubectl exec runs inside a pseudo-terminal so the shell sees a
// real TTY (otherwise readline / vim / less misbehave).
func (s *Server) wsExec(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	pod := r.PathValue("pod")
	container := r.URL.Query().Get("container")
	command := r.URL.Query().Get("command")
	if command == "" {
		// Same default the TUI uses: try bash, fall back to sh.
		command = "command -v bash >/dev/null && exec bash || exec sh"
	}
	cols := atoi(r.URL.Query().Get("cols"), 120)
	rows := atoi(r.URL.Query().Get("rows"), 30)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.allowedOrigins(),
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "")

	sess := s.readSession(r)
	args := []string{"exec", "-it", pod, "-n", ns}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--", "sh", "-c", command)
	args = sess.prependGlobalFlagsNoNS(args)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, kubectlPath(s), args...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		sendExit(ctx, conn, "could not start exec: "+err.Error())
		return
	}
	defer ptmx.Close()

	start := time.Now()
	s.recordWebAudit("exec", []string{pod}, 0, 0)

	var wg sync.WaitGroup
	wg.Add(2)

	// pty → websocket
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
					"type": "output", "data": string(buf[:n]),
				}))
			}
			if err != nil {
				if !isClosedPipe(err) {
					sendExit(ctx, conn, "session ended: "+err.Error())
				}
				sendExit(ctx, conn, "exit")
				cancel()
				return
			}
		}
	}()

	// websocket → pty
	go func() {
		defer wg.Done()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				_ = ptmx.Close()
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var frame struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				continue
			}
			switch frame.Type {
			case "input":
				_, _ = ptmx.Write([]byte(frame.Data))
			case "resize":
				if frame.Cols > 0 && frame.Rows > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(frame.Cols), Rows: uint16(frame.Rows)})
				}
			}
		}
	}()
	wg.Wait()
	_ = cmd.Wait()
	s.recordWebAudit("exec-end", []string{pod}, 0, time.Since(start))
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// allowedOrigins enumerates the Origin values we accept for the exec WS.
// Default: only same-origin (the URL the user opened).
func (s *Server) allowedOrigins() []string {
	out := []string{}
	for _, h := range s.allowedHosts() {
		out = append(out, h)
		if s.addr != "" {
			port := s.addr[strings.LastIndex(s.addr, ":")+1:]
			out = append(out, h+":"+port)
		}
	}
	return out
}

// kubectlPath returns the user's kubectl executable. The runner has already
// validated kubectl is on PATH at startup, so a bare "kubectl" works.
func kubectlPath(_ *Server) string { return "kubectl" }

// sendExit emits the final exit frame. We swallow errors — the connection
// may already be torn down.
func sendExit(ctx context.Context, conn *websocket.Conn, msg string) {
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]string{
		"type": "exit", "data": msg,
	}))
}

func isClosedPipe(err error) bool {
	if err == io.EOF {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "file already closed") || strings.Contains(s, "input/output error")
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
