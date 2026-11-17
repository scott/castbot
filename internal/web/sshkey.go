package web

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
	"golang.org/x/crypto/ssh"
)

const accountKeyURL = "https://api.digitalocean.com/v2/account/keys"

var errSSHKey = errors.New("ssh key is not registered")

type accountKey struct {
	ID          int64
	Name        string
	Fingerprint string
	PublicKey   string
}

// prepareDigitalOceanCreate attaches the account SSH fingerprint to a
// droplet create that did not name a key. Other calls are unchanged.
// The private seed is not written into the message.
func (s *Server) prepareDigitalOceanCreate(ctx context.Context, userID string, raw json.RawMessage) (json.RawMessage, error) {
	if !dropletCreateInvoke(raw) || dropletCreateHasKeys(raw) {
		return nil, nil
	}
	fp, err := s.ensureDigitalOceanSSH(ctx, userID)
	if err != nil || !store.SSHFingerprintOK(fp) {
		return nil, errSSHKey
	}
	next, err := injectDropletSSHKey(raw, fp)
	if err != nil {
		return nil, errSSHKey
	}
	return next, nil
}

// ensureDigitalOceanSSH returns the account fingerprint, creating and
// registering the key when this account does not have one yet.
func (s *Server) ensureDigitalOceanSSH(ctx context.Context, userID string) (string, error) {
	row, err := s.store.ConnectionSSHKey(ctx, userID, connect.DigitalOcean)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if err == nil && store.SSHFingerprintOK(row.Fingerprint) {
		return row.Fingerprint, nil
	}
	publicLine := ""
	if err == nil {
		publicLine = row.PublicKey
	}
	if publicLine == "" {
		var seed string
		publicLine, seed, err = newAccountSSHKey()
		if err != nil {
			return "", err
		}
		if err := s.store.SaveConnectionSSHMaterial(ctx, userID, connect.DigitalOcean, publicLine, seed); err != nil {
			return "", err
		}
	}
	localFP, err := sshLineFingerprint(publicLine)
	if err != nil || !store.SSHFingerprintOK(localFP) {
		return "", errSSHKey
	}
	token, err := s.store.ConnectionValue(ctx, userID, connect.DigitalOcean)
	if err != nil {
		return "", err
	}
	fp, remoteID, err := s.registerDigitalOceanKey(ctx, token, publicLine, localFP)
	if err != nil {
		return "", errSSHKey
	}
	if err := s.store.SetConnectionSSHRemote(ctx, userID, connect.DigitalOcean, fp, remoteID); err != nil {
		return "", err
	}
	return fp, nil
}

func (s *Server) registerDigitalOceanKey(ctx context.Context, token, publicLine, localFP string) (string, string, error) {
	if strings.TrimSpace(token) == "" || strings.Contains(publicLine, token) {
		return "", "", errSSHKey
	}
	keys, err := s.listAccountKeys(ctx, token)
	if err != nil {
		return "", "", err
	}
	if fp, id, ok := matchAccountKey(keys, localFP); ok {
		return fp, id, nil
	}
	created, status, err := s.createAccountKey(ctx, token, "castbot", publicLine, localFP)
	if err == nil {
		return created.Fingerprint, created.RemoteID, nil
	}
	keys, listErr := s.listAccountKeys(ctx, token)
	if listErr != nil {
		return "", "", listErr
	}
	if fp, id, ok := matchAccountKey(keys, localFP); ok {
		return fp, id, nil
	}
	taken := false
	for _, key := range keys {
		if key.Name == "castbot" {
			taken = true
			break
		}
	}
	if !taken && status != http.StatusUnprocessableEntity {
		return "", "", errSSHKey
	}
	created, _, err = s.createAccountKey(ctx, token, sshKeyAltName(localFP), publicLine, localFP)
	if err == nil {
		return created.Fingerprint, created.RemoteID, nil
	}
	keys, listErr = s.listAccountKeys(ctx, token)
	if listErr != nil {
		return "", "", listErr
	}
	if fp, id, ok := matchAccountKey(keys, localFP); ok {
		return fp, id, nil
	}
	return "", "", errSSHKey
}

func (s *Server) listAccountKeys(ctx context.Context, token string) ([]accountKey, error) {
	next := accountKeyURL + "?per_page=200"
	var all []accountKey
	for page := 0; page < 5 && next != ""; page++ {
		res, err := connect.Do(ctx, token, connect.Call{
			Connection: connect.DigitalOcean,
			Method:     http.MethodGet,
			URL:        next,
		}, s.dial())
		if err != nil || res.Status != http.StatusOK {
			return nil, errSSHKey
		}
		batch, follow, err := parseKeyPage(res.Body)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if follow == "" || !keysNextOK(follow) {
			break
		}
		next = follow
	}
	return all, nil
}

func (s *Server) createAccountKey(ctx context.Context, token, name, publicLine, localFP string) (struct {
	Fingerprint string
	RemoteID    string
}, int, error) {
	var none struct {
		Fingerprint string
		RemoteID    string
	}
	if name == "" || strings.ContainsAny(publicLine, "\n\r") || strings.Contains(publicLine, token) {
		return none, 0, errSSHKey
	}
	body, err := json.Marshal(map[string]string{"name": name, "public_key": publicLine})
	if err != nil {
		return none, 0, errSSHKey
	}
	res, err := connect.Do(ctx, token, connect.Call{
		Connection: connect.DigitalOcean,
		Method:     http.MethodPost,
		URL:        accountKeyURL,
		Body:       body,
	}, s.dial())
	if err != nil {
		return none, 0, errSSHKey
	}
	if res.Status != http.StatusOK && res.Status != http.StatusCreated {
		return none, res.Status, errSSHKey
	}
	created, err := parseCreatedKey(res.Body, localFP)
	if err != nil {
		return none, res.Status, err
	}
	return created, res.Status, nil
}

func parseKeyPage(body []byte) ([]accountKey, string, error) {
	var wrap struct {
		Keys []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Fingerprint string `json:"fingerprint"`
			PublicKey   string `json:"public_key"`
		} `json:"ssh_keys"`
		Links struct {
			Pages struct {
				Next string `json:"next"`
			} `json:"pages"`
		} `json:"links"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return nil, "", errSSHKey
	}
	out := make([]accountKey, 0, len(wrap.Keys))
	for _, key := range wrap.Keys {
		out = append(out, accountKey{ID: key.ID, Name: key.Name, Fingerprint: key.Fingerprint, PublicKey: key.PublicKey})
	}
	return out, wrap.Links.Pages.Next, nil
}

func parseCreatedKey(body []byte, localFP string) (struct {
	Fingerprint string
	RemoteID    string
}, error) {
	var none struct {
		Fingerprint string
		RemoteID    string
	}
	var wrap struct {
		Key struct {
			ID          int64  `json:"id"`
			Fingerprint string `json:"fingerprint"`
		} `json:"ssh_key"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return none, errSSHKey
	}
	fp := wrap.Key.Fingerprint
	if !store.SSHFingerprintOK(fp) {
		fp = localFP
	}
	if !store.SSHFingerprintOK(fp) {
		return none, errSSHKey
	}
	id := ""
	if wrap.Key.ID > 0 {
		id = strconv.FormatInt(wrap.Key.ID, 10)
	}
	return struct {
		Fingerprint string
		RemoteID    string
	}{Fingerprint: fp, RemoteID: id}, nil
}

func matchAccountKey(keys []accountKey, localFP string) (string, string, bool) {
	for _, key := range keys {
		fp, err := sshLineFingerprint(key.PublicKey)
		if err != nil || !strings.EqualFold(fp, localFP) {
			continue
		}
		chosen := key.Fingerprint
		if !store.SSHFingerprintOK(chosen) {
			chosen = localFP
		}
		id := ""
		if key.ID > 0 {
			id = strconv.FormatInt(key.ID, 10)
		}
		return chosen, id, true
	}
	return "", "", false
}

func keysNextOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "api.digitalocean.com") {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	path := u.EscapedPath()
	return path == "/v2/account/keys" || strings.HasPrefix(path, "/v2/account/keys/")
}

func sshKeyAltName(fp string) string {
	hex := strings.ReplaceAll(strings.ToLower(fp), ":", "")
	if len(hex) > 8 {
		hex = hex[:8]
	}
	return "castbot-" + hex
}

func newAccountSSHKey() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		return "", "", errSSHKey
	}
	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	return line + " castbot", seed, nil
}

func sshLineFingerprint(line string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil || pub == nil {
		return "", errSSHKey
	}
	return ssh.FingerprintLegacyMD5(pub), nil
}

func dropletCreateInvoke(raw json.RawMessage) bool {
	_, args, ok := rpcArgumentMap(raw)
	if !ok {
		return false
	}
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return false
	}
	if msg.Method != "tools/call" || strings.TrimSpace(msg.Params.Name) != "invoke_tool" {
		return false
	}
	return qualifiedDropletCreate(args)
}

func dropletCreateHasKeys(raw json.RawMessage) bool {
	_, args, ok := rpcArgumentMap(raw)
	if !ok {
		return false
	}
	if sshKeysPresent(args["SSHKeys"]) || sshKeysPresent(args["ssh_keys"]) {
		return true
	}
	nested, ok := args["arguments"].(map[string]any)
	if !ok {
		return false
	}
	return sshKeysPresent(nested["SSHKeys"]) || sshKeysPresent(nested["ssh_keys"])
}

func qualifiedDropletCreate(args map[string]any) bool {
	for _, key := range []string{"qualified_name", "qualifiedName"} {
		text, _ := args[key].(string)
		if dropletCreateName(text) {
			return true
		}
	}
	return false
}

func dropletCreateName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.Contains(name, "droplet-create")
}

func sshKeysPresent(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	default:
		return true
	}
}

func rpcArgumentMap(raw json.RawMessage) (map[string]any, map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var msg map[string]any
	if dec.Decode(&msg) != nil {
		return nil, nil, false
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		return nil, nil, false
	}
	args, _ := params["arguments"].(map[string]any)
	if args == nil {
		return msg, nil, false
	}
	return msg, args, true
}

func injectDropletSSHKey(raw json.RawMessage, fp string) (json.RawMessage, error) {
	if !store.SSHFingerprintOK(fp) {
		return nil, errSSHKey
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var msg map[string]any
	if err := dec.Decode(&msg); err != nil {
		return nil, err
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		return nil, errSSHKey
	}
	args, _ := params["arguments"].(map[string]any)
	if args == nil {
		return nil, errSSHKey
	}
	target := args
	if nested, ok := args["arguments"].(map[string]any); ok {
		target = nested
	}
	target["SSHKeys"] = []string{fp}
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return out, nil
}
