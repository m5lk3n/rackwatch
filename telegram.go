package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// telegramAPI is the Bot API's base URL; tests point it at a local server.
var telegramAPI = "https://api.telegram.org"

func (t telegramConfig) sendMessage(ctx context.Context, text string) error {
	form := url.Values{"chat_id": {string(t.ChatID)}, "text": {text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		telegramAPI+"/bot"+t.BotToken+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("invalid bot_token")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The request URL contains the bot token, so keep it out of the error.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !body.OK && body.Description != "" {
		return errors.New(body.Description)
	}
	if !body.OK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
