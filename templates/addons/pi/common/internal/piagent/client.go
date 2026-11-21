// Package piagent drives the Pi coding agent from Go.
//
// Pi's SDK is TypeScript-only, so this package uses the interface Pi documents
// for every other language: `pi --mode rpc`, a long-lived child process that
// reads JSON commands on stdin and writes JSON responses and events on stdout,
// one record per line. The package depends only on the standard library.
package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// closeGrace is how long Close waits for Pi to exit after its stdin is closed
// before killing it. Pi finishes pending work during an orderly shutdown.
const closeGrace = 10 * time.Second

// Options configures the Pi child process.
type Options struct {
	// Bin is the Pi executable. Empty means "pi", resolved on PATH.
	Bin string
	// Args are extra command-line arguments, such as "--model", "sonnet" or
	// "--no-tools". The RPC mode arguments are always supplied.
	Args []string
	// Dir is the working directory Pi's tools operate in. Empty means the
	// current directory.
	Dir string
	// Env is the child environment. Nil means the parent's environment,
	// which is where Pi reads provider keys such as ANTHROPIC_API_KEY.
	Env []string
	// Stderr receives Pi's diagnostics. Nil means os.Stderr. Stdout is
	// reserved for the protocol and is never exposed.
	Stderr io.Writer
}

// Event is one session event from Pi, such as "message_update" or
// "tool_execution_start". Raw holds the complete JSON record.
type Event struct {
	Type string
	Raw  json.RawMessage
}

// TextDelta returns the assistant text carried by a streaming event, or ""
// when the event carries none.
func (e Event) TextDelta() string {
	if e.Type != "message_update" {
		return ""
	}
	var record struct {
		Update struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(e.Raw, &record); err != nil || record.Update.Type != "text_delta" {
		return ""
	}
	return record.Update.Delta
}

// Result is the outcome of one prompt.
type Result struct {
	// Text is the text of the final assistant message.
	Text string
	// Aborted reports that the run ended because it was aborted.
	Aborted bool
}

// Client is a running Pi RPC session. A Client runs one prompt at a time and
// keeps the conversation between prompts. It is safe for concurrent use.
type Client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex // serializes records written to stdin
	runMu   sync.Mutex // one prompt run at a time

	mu       sync.Mutex
	nextID   int
	pending  map[string]chan response
	listener func(Event)

	done    chan struct{} // closed when the reader goroutine ends
	readErr error         // set before done is closed
}

type response struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// Start launches Pi in RPC mode. The caller must call Close.
func Start(opts Options) (*Client, error) {
	bin := opts.Bin
	if bin == "" {
		bin = "pi"
	}
	args := append([]string{"--mode", "rpc", "--no-session"}, opts.Args...)

	cmd := exec.Command(bin, args...) // #nosec G204 -- the caller chooses the agent binary and its arguments
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stderr = opts.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("piagent: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("piagent: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("piagent: start %s: %w", bin, err)
	}

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan response),
		done:    make(chan struct{}),
	}
	go c.read(stdout)

	return c, nil
}

// read is the only reader of Pi's stdout. It must never stop early: Pi honors
// stdout backpressure, so an unread pipe stalls the agent.
func (c *Client) read(stdout io.Reader) {
	// bufio.Reader, not bufio.Scanner: one record can hold a whole file and
	// exceed Scanner's token limit. Records are split on LF only.
	reader := bufio.NewReader(stdout)
	var err error
	for {
		var line []byte
		line, err = reader.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	if errors.Is(err, io.EOF) {
		err = errors.New("piagent: pi exited")
	}

	c.mu.Lock()
	c.readErr = err
	c.mu.Unlock()
	close(c.done)
}

func (c *Client) dispatch(line []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return // not a protocol record; ignore it rather than abort the session
	}

	if head.Type == "response" {
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			return
		}
		c.mu.Lock()
		waiter := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if waiter != nil {
			waiter <- resp
		}
		return
	}

	c.mu.Lock()
	listener := c.listener
	c.mu.Unlock()
	if listener != nil {
		listener(Event{Type: head.Type, Raw: append(json.RawMessage(nil), line...)})
	}
}

// call sends one command and waits for its response.
func (c *Client) call(ctx context.Context, command map[string]any) (response, error) {
	c.mu.Lock()
	c.nextID++
	id := "req-" + strconv.Itoa(c.nextID)
	waiter := make(chan response, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	command["id"] = id
	record, err := json.Marshal(command)
	if err != nil {
		forget()
		return response{}, fmt.Errorf("piagent: encode command: %w", err)
	}

	c.writeMu.Lock()
	_, err = c.stdin.Write(append(record, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		forget()
		return response{}, fmt.Errorf("piagent: write command: %w", err)
	}

	select {
	case resp := <-waiter:
		if !resp.Success {
			return resp, fmt.Errorf("piagent: %s rejected: %s", resp.Command, resp.Error)
		}
		return resp, nil
	case <-c.done:
		forget()
		return response{}, c.exitErr()
	case <-ctx.Done():
		forget()
		return response{}, ctx.Err()
	}
}

func (c *Client) exitErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

// Prompt sends one prompt and blocks until Pi has no work left for it.
//
// onEvent, when non-nil, receives every session event of the run in order; use
// Event.TextDelta to stream the answer. It is called on the reader goroutine,
// so it must return promptly and must not call back into the Client.
//
// A run that the model provider fails is an error. Cancelling ctx aborts the
// run and returns ctx.Err.
func (c *Client) Prompt(ctx context.Context, message string, onEvent func(Event)) (Result, error) {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	var (
		text     strings.Builder
		runErr   string
		aborted  bool
		settled  = make(chan struct{})
		finished bool
	)
	// The listener is installed before the prompt is sent so a fast run
	// cannot settle unseen. It runs on the reader goroutine only.
	c.setListener(func(event Event) {
		if finished {
			return
		}
		switch event.Type {
		case "message_start":
			if role, _ := messageInfo(event.Raw); role == "assistant" {
				text.Reset()
				runErr = ""
			}
		case "message_update":
			text.WriteString(event.TextDelta())
		case "message_end":
			if role, failure := messageInfo(event.Raw); role == "assistant" {
				runErr = failure
			}
		case "agent_settled":
			var record struct {
				Aborted bool `json:"aborted"`
			}
			_ = json.Unmarshal(event.Raw, &record)
			aborted = record.Aborted
		}
		if onEvent != nil {
			onEvent(event)
		}
		// Pi can retry or continue after agent_end; only agent_settled
		// means it will do nothing more for this prompt.
		if event.Type == "agent_settled" {
			finished = true
			close(settled)
		}
	})
	defer c.setListener(nil)

	resp, err := c.call(ctx, map[string]any{"type": "prompt", "message": message})
	if err != nil {
		return Result{}, err
	}
	var accepted struct {
		Disposition string `json:"disposition"`
	}
	_ = json.Unmarshal(resp.Data, &accepted)
	if accepted.Disposition == "handled" {
		// An extension consumed the prompt; no run started, so there is
		// no agent_settled to wait for.
		return Result{}, nil
	}

	select {
	case <-settled:
	case <-c.done:
		return Result{}, c.exitErr()
	case <-ctx.Done():
		// Best effort: tell Pi to stop, but do not wait on a context that
		// is already done.
		abortCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = c.Abort(abortCtx)
		cancel()
		return Result{}, ctx.Err()
	}

	// The reader goroutine finished writing these before it closed settled.
	if runErr != "" && !aborted {
		return Result{}, fmt.Errorf("piagent: run failed: %s", runErr)
	}
	return Result{Text: text.String(), Aborted: aborted}, nil
}

func (c *Client) setListener(listener func(Event)) {
	c.mu.Lock()
	c.listener = listener
	c.mu.Unlock()
}

// messageInfo reads the role of a message event and, when the provider failed
// the message, why.
func messageInfo(raw json.RawMessage) (role string, failure string) {
	var record struct {
		Message struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return "", ""
	}
	if record.Message.StopReason == "error" {
		failure = record.Message.ErrorMessage
		if failure == "" {
			failure = "the model provider reported an error"
		}
	}
	return record.Message.Role, failure
}

// Abort stops the current run and waits for Pi to become idle.
func (c *Client) Abort(ctx context.Context) error {
	_, err := c.call(ctx, map[string]any{"type": "abort"})
	return err
}

// Close shuts Pi down: it closes stdin, which asks for an orderly exit, and
// kills the process if it has not exited after a grace period.
func (c *Client) Close() error {
	c.writeMu.Lock()
	closeErr := c.stdin.Close()
	c.writeMu.Unlock()

	select {
	case <-c.done:
	case <-time.After(closeGrace):
		_ = c.cmd.Process.Kill()
		<-c.done
	}

	// The exit status of a child that was asked to stop carries no signal
	// the caller can act on; reap it and report only a failed stdin close.
	_ = c.cmd.Wait()
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return fmt.Errorf("piagent: close stdin: %w", closeErr)
	}
	return nil
}
