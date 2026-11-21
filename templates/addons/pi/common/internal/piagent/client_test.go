package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// fakePiEnv selects the behaviour of the fake Pi process. The test binary
// re-executes itself as that process (see TestMain), so the tests exercise
// the real process and pipe handling without Node, Pi, or a network.
const fakePiEnv = "PIAGENT_FAKE_PI"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakePiEnv); mode != "" {
		runFakePi(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func emit(record string) {
	_, _ = fmt.Fprintln(os.Stdout, record)
}

// runFakePi speaks just enough of Pi's RPC protocol for the tests. Record
// shapes are copied from a real `pi --mode rpc` session.
func runFakePi(mode string) {
	if mode == "exit" {
		return
	}

	input := bufio.NewReader(os.Stdin)
	for {
		line, err := input.ReadBytes('\n')
		if err != nil {
			return
		}
		var command struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &command); err != nil {
			emit(`{"type":"response","command":"parse","success":false,"error":"Failed to parse command"}`)
			continue
		}

		respond := func(body string) {
			emit(fmt.Sprintf(`{"id":%q,"type":"response","command":%q,%s}`, command.ID, command.Type, body))
		}

		switch {
		case command.Type == "abort":
			respond(`"success":true`)
			emit(`{"type":"agent_settled","aborted":true}`)
		case command.Type != "prompt":
			respond(`"success":false,"error":"unsupported command"`)
		case mode == "reject":
			respond(`"success":false,"error":"No model selected"`)
		case mode == "handled":
			respond(`"success":true,"data":{"disposition":"handled"}`)
		case mode == "hang":
			respond(`"success":true,"data":{"disposition":"started"}`)
			emit(`{"type":"agent_start"}`)
		case mode == "provider-error":
			respond(`"success":true,"data":{"disposition":"started"}`)
			emit(`{"type":"agent_start"}`)
			emit(`{"type":"message_start","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"invalid api key"}}`)
			emit(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"invalid api key"}}`)
			emit(`{"type":"agent_end","messages":[]}`)
			emit(`{"type":"agent_settled","aborted":false}`)
		default: // "ok": a tool-calling turn, then the answer.
			respond(`"success":true,"data":{"disposition":"started"}`)
			emit(`{"type":"agent_start"}`)
			emit(`{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
			emit(`{"type":"message_start","message":{"role":"assistant","content":[]}}`)
			emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Let me look."}}`)
			emit(`{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse"}}`)
			emit(`{"type":"tool_execution_start","toolName":"read"}`)
			emit(`{"type":"message_start","message":{"role":"assistant","content":[]}}`)
			emit(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"echo: "}}`)
			// A record far past bufio.Scanner's 64 KiB token limit.
			emit(`{"type":"tool_execution_update","output":"` + strings.Repeat("x", 200_000) + `"}`)
			emit(fmt.Sprintf(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":%q}}`, command.Message))
			emit(`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`)
			emit(`{"type":"agent_end","messages":[]}`)
			emit(`{"type":"agent_settled","aborted":false}`)
		}
	}
}

func startFakePi(t *testing.T, mode string) *Client {
	t.Helper()

	client, err := Start(Options{
		Bin:    os.Args[0],
		Env:    append(os.Environ(), fakePiEnv+"="+mode),
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return client
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPromptReturnsTheFinalAssistantTextAndStreamsEvents(t *testing.T) {
	client := startFakePi(t, "ok")

	var streamed strings.Builder
	var types []string
	result, err := client.Prompt(testContext(t), "hello", func(event Event) {
		types = append(types, event.Type)
		streamed.WriteString(event.TextDelta())
	})
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	if want := "echo: hello"; result.Text != want {
		t.Fatalf("Result.Text = %q, want %q (only the final assistant message)", result.Text, want)
	}
	if result.Aborted {
		t.Fatal("Result.Aborted = true, want false")
	}
	if want := "Let me look.echo: hello"; streamed.String() != want {
		t.Fatalf("streamed text = %q, want %q", streamed.String(), want)
	}
	if last := types[len(types)-1]; last != "agent_settled" {
		t.Fatalf("last event = %q, want agent_settled", last)
	}
}

func TestClientKeepsTheSessionAcrossPrompts(t *testing.T) {
	client := startFakePi(t, "ok")

	for _, message := range []string{"one", "two"} {
		result, err := client.Prompt(testContext(t), message, nil)
		if err != nil {
			t.Fatalf("Prompt(%q) error = %v", message, err)
		}
		if want := "echo: " + message; result.Text != want {
			t.Fatalf("Prompt(%q) text = %q, want %q", message, result.Text, want)
		}
	}
}

func TestPromptReportsARejectedCommand(t *testing.T) {
	client := startFakePi(t, "reject")

	_, err := client.Prompt(testContext(t), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "No model selected") {
		t.Fatalf("Prompt() error = %v, want the rejection reason", err)
	}
}

func TestPromptReportsAProviderFailure(t *testing.T) {
	client := startFakePi(t, "provider-error")

	_, err := client.Prompt(testContext(t), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("Prompt() error = %v, want the provider error", err)
	}
}

func TestPromptReturnsWhenAnExtensionHandlesThePrompt(t *testing.T) {
	client := startFakePi(t, "handled")

	result, err := client.Prompt(testContext(t), "/command", nil)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if result.Text != "" {
		t.Fatalf("Result.Text = %q, want empty", result.Text)
	}
}

func TestPromptStopsWhenTheContextIsCancelled(t *testing.T) {
	client := startFakePi(t, "hang")

	ctx, cancel := context.WithCancel(testContext(t))
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()

	_, err := client.Prompt(ctx, "hello", func(event Event) {
		if event.Type == "agent_start" {
			close(started)
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prompt() error = %v, want context.Canceled", err)
	}
}

func TestPromptReportsAnUnexpectedExit(t *testing.T) {
	client := startFakePi(t, "exit")

	_, err := client.Prompt(testContext(t), "hello", nil)
	if err == nil {
		t.Fatal("Prompt() error = nil, want an error when pi exits")
	}
}

func TestStartReportsAMissingBinary(t *testing.T) {
	_, err := Start(Options{Bin: "piagent-no-such-binary"})
	if err == nil {
		t.Fatal("Start() error = nil, want an error for a missing binary")
	}
}
