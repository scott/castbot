package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// RuntimeImagePrefix is the public machine image named in a release manifest.
const RuntimeImagePrefix = "ghcr.io/scott/castbot-runtime:"

// ImagePlan is what an update should do with CASTBOT_IMAGE.
// Move sets the line to Next. Block stops before any write.
// A zero plan updates the control plane and leaves the line as it is.
type ImagePlan struct {
	Move  bool
	Block bool
	Next  string
}

// DecideImage applies the machine image policy.
// visible is called only when the installed tag is the public image and it
// differs from the release. An error from visible is an incomplete check.
func DecideImage(ctx context.Context, installed, releaseCommit, runtimeImage, runtimeImageCommit string, visible func(context.Context, string) (bool, error)) (ImagePlan, error) {
	installed = strings.TrimSpace(installed)
	if installed == "" {
		return ImagePlan{}, nil
	}
	if strings.ContainsAny(installed, " \t\r\n") {
		return ImagePlan{Block: true}, nil
	}
	if tag, ok := officialTag(installed); ok {
		if tag == runtimeImageCommit && shaRE.MatchString(runtimeImageCommit) {
			return ImagePlan{}, nil
		}
		if visible == nil {
			return ImagePlan{}, errors.New("runtime image lookup failed")
		}
		ok, err := visible(ctx, runtimeImage)
		if err != nil {
			return ImagePlan{}, err
		}
		if ok && runtimeImage == RuntimeImagePrefix+runtimeImageCommit && shaRE.MatchString(runtimeImageCommit) {
			return ImagePlan{Move: true, Next: runtimeImage}, nil
		}
		return ImagePlan{Block: true}, nil
	}
	tag, ok := imageTag(installed)
	if !ok || !shaRE.MatchString(tag) {
		return ImagePlan{Block: true}, nil
	}
	if (tag == releaseCommit || tag == runtimeImageCommit) && shaRE.MatchString(tag) {
		return ImagePlan{}, nil
	}
	return ImagePlan{Block: true}, nil
}

func officialTag(ref string) (string, bool) {
	rest, ok := strings.CutPrefix(ref, RuntimeImagePrefix)
	if !ok || !shaRE.MatchString(rest) {
		return "", false
	}
	return rest, true
}

func imageTag(ref string) (string, bool) {
	if strings.Contains(ref, "@") {
		return "", false
	}
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", false
	}
	return ref[i+1:], true
}

func (c *Checker) imageVisible(ctx context.Context, image string) (bool, error) {
	if c.Visible != nil {
		return c.Visible(ctx, image)
	}
	return c.ghcrVisible(ctx, image)
}

// ghcrVisible reports whether the public registry lists this image tag.
// A denied anonymous token is not visible. Other failures are errors.
func (c *Checker) ghcrVisible(ctx context.Context, image string) (bool, error) {
	tag, ok := officialTag(image)
	if !ok {
		return false, nil
	}
	base := strings.TrimRight(c.RegistryBase, "/")
	if base == "" {
		base = "https://ghcr.io"
	}
	if c.Client == nil && base != "https://ghcr.io" {
		return false, errors.New("runtime image lookup failed")
	}
	tokenURL := base + "/token?service=ghcr.io&scope=repository:scott/castbot-runtime:pull"
	body, code, err := c.exchange(ctx, tokenURL, "", 64<<10)
	if err != nil {
		return false, errors.New("runtime image lookup failed")
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return false, nil
	}
	if code != http.StatusOK {
		return false, errors.New("runtime image lookup failed")
	}
	var payload struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Token == "" {
		return false, errors.New("runtime image lookup failed")
	}
	manifestURL := base + "/v2/scott/castbot-runtime/manifests/" + tag
	_, code, err = c.exchange(ctx, manifestURL, payload.Token, 1<<20)
	if err != nil {
		return false, errors.New("runtime image lookup failed")
	}
	switch code {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false, nil
	default:
		return false, errors.New("runtime image lookup failed")
	}
}

func (c *Checker) exchange(ctx context.Context, raw, token string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, errors.New("response is too large")
	}
	return body, resp.StatusCode, nil
}
