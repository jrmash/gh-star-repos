// Package github discovers repositories and manages stars through GitHub CLI.
package github

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jrmash/gh-star-repos/internal/executil"
)

// Client keeps request policy separate from command execution. It is sequential
// by design; its clock, sleeper, and runner are replaceable in tests.
type Client struct {
	Runner executil.Runner
	Host   string
	Pause  time.Duration
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
	OnWait func(time.Duration, string)

	lastMutation time.Time
	hasMutation  bool
	notBefore    time.Time
}

type APIError struct {
	Message     string
	Status      int
	Headers     http.Header
	Retryable   bool
	RateLimited bool
	Fatal       bool
}

func (e *APIError) Error() string { return e.Message }

func Fatal(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Fatal
}

func Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Do retries only transient and rate-limit errors, up to three times. recover
// reconciles non-idempotent list creation before another mutation is attempted.
func (c *Client) Do(ctx context.Context, query string, variables map[string]any, out any, mutation bool, recover func(context.Context) (bool, error)) error {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = Sleep
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if delay := c.notBefore.Sub(c.Now()); delay > 0 {
			if err := c.wait(ctx, delay, "GitHub rate limit reset"); err != nil {
				return err
			}
		}
		if mutation && c.hasMutation {
			if err := c.wait(ctx, c.Pause-c.Now().Sub(c.lastMutation), "between mutations"); err != nil {
				return err
			}
		}
		err := c.request(ctx, query, variables, out)
		if mutation {
			c.lastMutation, c.hasMutation = c.Now(), true
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Fatal || !apiErr.Retryable || attempt == 3 {
			if apiErr != nil && apiErr.RateLimited {
				apiErr.Fatal = true
			}
			return err
		}
		if err := c.wait(ctx, c.retryDelay(apiErr, attempt), "retrying GitHub request"); err != nil {
			return err
		}
		if recover != nil {
			done, err := recover(ctx)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

func (c *Client) wait(ctx context.Context, delay time.Duration, reason string) error {
	if delay <= 0 {
		return ctx.Err()
	}
	if c.OnWait != nil {
		c.OnWait(delay, reason)
	}
	return c.Sleep(ctx, delay)
}

func (c *Client) retryDelay(err *APIError, attempt int) time.Duration {
	delay := time.Second << attempt
	if err.RateLimited {
		delay = time.Minute << attempt
	}
	if value := err.Headers.Get("Retry-After"); value != "" {
		if seconds, parseErr := strconv.ParseInt(value, 10, 32); parseErr == nil && seconds >= 0 {
			return max(time.Duration(seconds)*time.Second, c.notBefore.Sub(c.Now()))
		}
		if date, parseErr := http.ParseTime(value); parseErr == nil {
			return max(date.Sub(c.Now()), c.notBefore.Sub(c.Now()), time.Duration(0))
		}
	}
	if err.Headers.Get("X-Ratelimit-Remaining") == "0" {
		if seconds, parseErr := strconv.ParseInt(err.Headers.Get("X-Ratelimit-Reset"), 10, 64); parseErr == nil {
			return max(time.Unix(seconds, 0).Sub(c.Now()), time.Duration(0))
		}
	}
	return delay
}

func (c *Client) request(ctx context.Context, query string, variables map[string]any, out any) error {
	args := []string{"api", "graphql", "--method", "POST", "--include", "--hostname", c.Host,
		"--header", "Accept: application/vnd.github+json", "--raw-field", "query=" + query}
	keys := make([]string, 0, len(variables))
	for key := range variables {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		switch v := variables[key].(type) {
		case string:
			args = append(args, "--raw-field", key+"="+v)
		case []string:
			for _, item := range v {
				args = append(args, "--raw-field", key+"[]="+item)
			}
		default:
			text, err := json.Marshal(v)
			if err != nil {
				return err
			}
			args = append(args, "--field", key+"="+string(text))
		}
	}
	output, commandErr := c.Runner.Run(ctx, "gh", args...)
	status, headers, body, err := splitResponse(output)
	if err != nil {
		return err
	}
	if headers.Get("X-Ratelimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(headers.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
			c.notBefore = time.Unix(reset, 0)
		}
	}
	var response struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string         `json:"message"`
			Type       string         `json:"type"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
		Message string `json:"message"`
	}
	decodeErr := json.Unmarshal([]byte(body), &response)
	if status >= 400 || len(response.Errors) > 0 || commandErr != nil {
		apiErr := &APIError{Status: status, Headers: headers}
		messages := []string{}
		if response.Message != "" {
			messages = append(messages, response.Message)
		}
		for _, item := range response.Errors {
			messages = append(messages, item.Message)
			kind := strings.ToUpper(item.Type)
			if kind == "" {
				kind, _ = item.Extensions["code"].(string)
				kind = strings.ToUpper(kind)
			}
			switch kind {
			case "RATE_LIMITED", "RATE_LIMITED_ERROR", "RATE_LIMIT_EXCEEDED":
				apiErr.RateLimited = true
			case "UNAUTHORIZED", "UNAUTHENTICATED", "BAD_CREDENTIALS":
				apiErr.Fatal = true
			case "INTERNAL", "INTERNAL_SERVER_ERROR", "SERVICE_UNAVAILABLE":
				apiErr.Retryable = true
			}
		}
		if len(messages) == 0 {
			if commandErr != nil {
				messages = append(messages, commandErr.Error())
			} else {
				messages = append(messages, fmt.Sprintf("GitHub returned HTTP %d", status))
			}
		}
		apiErr.Message = strings.Join(messages, "; ")
		lower := strings.ToLower(apiErr.Message)
		apiErr.RateLimited = apiErr.RateLimited || status == 429 || status == 403 &&
			(headers.Get("X-Ratelimit-Remaining") == "0" || headers.Get("Retry-After") != "" || strings.Contains(lower, "rate limit"))
		apiErr.Fatal = apiErr.Fatal || status == 401 || strings.Contains(lower, "bad credentials")
		var exit *exec.ExitError
		if errors.As(commandErr, &exit) && exit.ExitCode() == 4 {
			apiErr.Fatal = true
		}
		apiErr.Retryable = apiErr.Retryable || apiErr.RateLimited || status == 408 || status >= 500
		if status == 0 && commandErr != nil {
			for _, hint := range []string{"timeout", "connection reset", "connection refused", "unexpected eof", "no such host", "temporary failure", "tls handshake"} {
				apiErr.Retryable = apiErr.Retryable || strings.Contains(lower, hint)
			}
		}
		return apiErr
	}
	if decodeErr != nil {
		if strings.Contains(decodeErr.Error(), "unexpected end of JSON input") {
			return &APIError{Message: "truncated GitHub response", Headers: headers, Retryable: true}
		}
		return fmt.Errorf("decode GitHub response: %w", decodeErr)
	}
	if len(response.Data) == 0 || string(response.Data) == "null" {
		return errors.New("GitHub returned no GraphQL data")
	}
	if err := json.Unmarshal(response.Data, out); err != nil {
		return fmt.Errorf("decode GraphQL data: %w", err)
	}
	return nil
}

// gh api --include emits already-decoded bodies; parsing the header block
// separately avoids treating its body as HTTP chunked transfer encoding.
func splitResponse(output string) (int, http.Header, string, error) {
	if !strings.HasPrefix(output, "HTTP/") {
		return 0, http.Header{}, output, nil
	}
	reader := bufio.NewReader(strings.NewReader(output))
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, nil, "", fmt.Errorf("read HTTP status: %w", err)
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, nil, "", errors.New("invalid GitHub HTTP status")
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, "", errors.New("invalid GitHub HTTP status")
	}
	headers, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		return 0, nil, "", fmt.Errorf("read GitHub HTTP headers: %w", err)
	}
	body, err := io.ReadAll(reader)
	return status, http.Header(headers), string(body), err
}
