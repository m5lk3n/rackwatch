package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// publish posts text to the topic on the ntfy server, ntfy.sh or a self-hosted one.
func (n ntfyConfig) publish(ctx context.Context, text string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.topicURL(), strings.NewReader(text))
	if err != nil {
		return errors.New("invalid server or topic")
	}
	req.Header.Set("Title", "rackwatch")
	if n.Priority != 0 {
		req.Header.Set("Priority", strconv.Itoa(n.Priority))
	}
	switch {
	case n.Token != "":
		req.Header.Set("Authorization", "Bearer "+n.Token)
	case n.Username != "":
		req.SetBasicAuth(n.Username, n.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// On ntfy.sh the topic name is the only secret, so keep the URL out of the error.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil || body.Error == "" {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return errors.New(body.Error)
}

func (n ntfyConfig) topicURL() string {
	return strings.TrimRight(n.Server, "/") + "/" + n.Topic
}
