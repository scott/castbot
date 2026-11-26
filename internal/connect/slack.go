package connect

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const (
	Slack           = "slack"
	SecretNameSlack = "SLACK_CONNECTION"
)

func init() {
	register(slackEntry())
}

func slackEntry() Entry {
	return Entry{
		ID: Slack, Title: "Slack", SecretName: SecretNameSlack,
		Detail:     "Bot token for channels the bot has joined. Direct messages stay off. Stored in your vault.",
		TokenLabel: "Bot token",
		Hosts:      []string{"slack.com"},
		APIHost:    "slack.com",
		Auth:       AuthBearer,
		Aliases:    []string{"SLACK_BOT_TOKEN", "SLACK_TOKEN", "SLACK_API_TOKEN"},
		Tools:      slackTools,
	}
}

var slackTools = []Tool{
	{
		Name: "slack_list_channels", Title: "Slack: list channels", Method: "GET",
		Args:  []Arg{{Name: "limit", Description: "How many channels, at most 100."}},
		Build: slackListCall, ReadBody: slackChannelsBody,
	},
	{
		Name: "slack_read_channel", Title: "Slack: read a channel", Method: "GET",
		Args: []Arg{
			{Name: "channel", Description: "Channel id.", Required: true},
			{Name: "limit", Description: "How many messages, at most 20."},
		},
		Build: slackHistoryCall, ReadBody: slackMessagesBody,
	},
	{
		Name: "slack_read_thread", Title: "Slack: read a thread", Method: "GET",
		Args: []Arg{
			{Name: "channel", Description: "Channel id.", Required: true},
			{Name: "ts", Description: "Thread timestamp.", Required: true},
			{Name: "limit", Description: "How many messages, at most 20."},
		},
		Build: slackThreadCall, ReadBody: slackMessagesBody,
	},
	{
		Name: "slack_post", Title: "Slack: post", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "channel", Description: "Channel id.", Required: true},
			{Name: "text", Description: "Message text.", Required: true},
		},
		Build: slackPostCall, ReadBody: slackPostBody,
	},
	{
		Name: "slack_reply", Title: "Slack: reply", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "channel", Description: "Channel id.", Required: true},
			{Name: "ts", Description: "Thread timestamp.", Required: true},
			{Name: "text", Description: "Reply text.", Required: true},
		},
		Build: slackReplyCall, ReadBody: slackPostBody,
	},
}

func slackListCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := slackLimit(args, 100, 100)
	if err != nil {
		return Call{}, err
	}
	q := url.Values{}
	q.Set("exclude_archived", "true")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("types", "public_channel,private_channel")
	return slackGet("conversations.list", q)
}

func slackHistoryCall(_ context.Context, args map[string]any) (Call, error) {
	channel, err := slackChannel(args)
	if err != nil {
		return Call{}, err
	}
	limit, err := slackLimit(args, 20, 20)
	if err != nil {
		return Call{}, err
	}
	q := url.Values{}
	q.Set("channel", channel)
	q.Set("limit", strconv.Itoa(limit))
	return slackGet("conversations.history", q)
}

func slackThreadCall(_ context.Context, args map[string]any) (Call, error) {
	channel, err := slackChannel(args)
	if err != nil {
		return Call{}, err
	}
	ts, err := slackTS(args)
	if err != nil {
		return Call{}, err
	}
	limit, err := slackLimit(args, 20, 20)
	if err != nil {
		return Call{}, err
	}
	q := url.Values{}
	q.Set("channel", channel)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("ts", ts)
	return slackGet("conversations.replies", q)
}

func slackPostCall(_ context.Context, args map[string]any) (Call, error) {
	channel, err := slackChannel(args)
	if err != nil {
		return Call{}, err
	}
	text, err := slackText(args)
	if err != nil {
		return Call{}, err
	}
	return slackPost(map[string]any{"channel": channel, "text": text})
}

func slackReplyCall(_ context.Context, args map[string]any) (Call, error) {
	channel, err := slackChannel(args)
	if err != nil {
		return Call{}, err
	}
	ts, err := slackTS(args)
	if err != nil {
		return Call{}, err
	}
	text, err := slackText(args)
	if err != nil {
		return Call{}, err
	}
	return slackPost(map[string]any{"channel": channel, "text": text, "thread_ts": ts})
}

func slackGet(method string, q url.Values) (Call, error) {
	return Call{
		Method:  "GET",
		URL:     "https://slack.com/api/" + method + "?" + q.Encode(),
		Headers: map[string]string{"Accept": "application/json"},
	}, nil
}

func slackPost(payload map[string]any) (Call, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Call{}, ErrBad
	}
	return Call{
		Method:  "POST",
		URL:     "https://slack.com/api/chat.postMessage",
		Headers: map[string]string{"Accept": "application/json", "Content-Type": "application/json"},
		Body:    body,
	}, nil
}

func slackChannel(args map[string]any) (string, error) {
	id, ok := argString(args, "channel")
	if !ok || len(id) < 2 || len(id) > 32 || (id[0] != 'C' && id[0] != 'G') {
		return "", ErrBad
	}
	for _, r := range id[1:] {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return "", ErrBad
		}
	}
	return id, nil
}

func slackTS(args map[string]any) (string, error) {
	ts, ok := argString(args, "ts")
	if !ok || ts == "" || len(ts) > 32 {
		return "", ErrBad
	}
	dot := false
	digits := 0
	for _, r := range ts {
		if r == '.' {
			if dot || digits == 0 {
				return "", ErrBad
			}
			dot = true
			digits = 0
			continue
		}
		if r < '0' || r > '9' {
			return "", ErrBad
		}
		digits++
	}
	if digits == 0 {
		return "", ErrBad
	}
	return ts, nil
}

func slackText(args map[string]any) (string, error) {
	text, ok := argString(args, "text")
	if !ok || text == "" || len(text) > 4000 || strings.ContainsAny(text, "\r\x00") {
		return "", ErrBad
	}
	return text, nil
}

func slackLimit(args map[string]any, fallback, max int) (int, error) {
	if args != nil {
		if v, exists := args["limit"]; exists && v != nil {
			raw, ok := argString(args, "limit")
			if !ok || raw == "" {
				return 0, ErrBad
			}
			n := 0
			for _, r := range raw {
				if r < '0' || r > '9' || n > 1000 {
					return 0, ErrBad
				}
				n = n*10 + int(r-'0')
			}
			if n < 1 {
				return 0, ErrBad
			}
			if n > max {
				n = max
			}
			return n, nil
		}
	}
	return fallback, nil
}

func slackClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

func slackChannelsBody(body []byte) []byte {
	var raw struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channels []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Private bool   `json:"is_private"`
		} `json:"channels"`
	}
	if json.Unmarshal(slackClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	type channel struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Private bool   `json:"is_private"`
	}
	out := []channel{}
	for i, item := range raw.Channels {
		if i >= 100 {
			break
		}
		out = append(out, channel{ID: item.ID, Name: clipText(item.Name, 80), Private: item.Private})
	}
	buf, err := json.Marshal(map[string]any{"ok": raw.OK, "error": raw.Error, "channels": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func slackMessagesBody(body []byte) []byte {
	var raw struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Messages []struct {
			User string `json:"user"`
			Text string `json:"text"`
			TS   string `json:"ts"`
		} `json:"messages"`
	}
	if json.Unmarshal(slackClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	type message struct {
		User string `json:"user"`
		Text string `json:"text"`
		TS   string `json:"ts"`
	}
	out := []message{}
	for i, item := range raw.Messages {
		if i >= 20 {
			break
		}
		out = append(out, message{User: item.User, Text: clipText(item.Text, 2000), TS: item.TS})
	}
	buf, err := json.Marshal(map[string]any{"ok": raw.OK, "error": raw.Error, "messages": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func slackPostBody(body []byte) []byte {
	var raw struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if json.Unmarshal(slackClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]any{"ok": raw.OK, "error": raw.Error, "channel": raw.Channel, "ts": raw.TS})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}
