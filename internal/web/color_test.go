package web

import (
	"context"
	"html"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/store"
)

func TestBubbleContrast(t *testing.T) {
	keys := store.PaletteKeys()
	rows := agentPalette()
	if len(keys) != len(rows) {
		t.Fatalf("keys %d palette %d", len(keys), len(rows))
	}
	for i, c := range rows {
		if keys[i] != c.Key {
			t.Fatalf("key %s vs %s", keys[i], c.Key)
		}
		avatar := contrastRatio(c.Avatar, avatarInk)
		dark := contrastRatio(c.DarkFill, c.DarkText)
		light := contrastRatio(c.LightFill, c.LightText)
		if dark < 4.5 || light < 4.5 || avatar < 4.5 {
			t.Fatalf("%s avatar %.2f dark %.2f light %.2f", c.Key, avatar, dark, light)
		}
		if math.Abs(avatar-c.AvatarRatio) > 0.02 || math.Abs(dark-c.DarkRatio) > 0.02 || math.Abs(light-c.LightRatio) > 0.02 {
			t.Fatalf("%s measured avatar %.2f dark %.2f light %.2f", c.Key, avatar, dark, light)
		}
	}
}

func TestPaletteCSSMatchesTable(t *testing.T) {
	raw, err := os.ReadFile("static/castbot.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	if strings.Contains(css, "color-mix(") && strings.Contains(css[strings.Index(css, "per-agent colors"):], "color-mix(") {
		t.Fatal("bubble rules use color-mix")
	}
	for _, c := range agentPalette() {
		avatar := ".av." + c.Key + " { background: " + c.Avatar + "; }"
		dark := ".m-user." + c.Key + " { background: " + c.DarkFill + "; border-color: " + c.DarkBorder + "; color: " + c.DarkText + "; }"
		light := "html.light .m-user." + c.Key + " { background: " + c.LightFill + "; border-color: " + c.LightBorder + "; color: " + c.LightText + "; }"
		queued := ".m-user." + c.Key + ".queued { background: transparent; border-style: dashed; border-color: " + c.DarkBorder + "; color: var(--tx2); }"
		lightQueued := "html.light .m-user." + c.Key + ".queued { border-color: " + c.LightBorder + "; }"
		for _, rule := range []string{avatar, dark, light, queued, lightQueued} {
			if !strings.Contains(css, rule) {
				t.Fatalf("missing %s", rule)
			}
		}
	}
	if !strings.Contains(css, "rgba(0,0,0,.18)") || !strings.Contains(css, "rgba(0,0,0,.06)") {
		t.Fatal("inline code background")
	}
}

func TestChatCreateStoresAutoColor(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("chat-color@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.227:9"
	h.login("chat-color@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Color: "c1", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	if !strings.Contains(body, `class="av c2"`) {
		t.Fatal("setup card did not show the auto-picked color")
	}
	if strings.Contains(body, `class="swatches"`) || strings.Contains(body, `name="color"`) {
		t.Fatal("setup card has a swatch row")
	}
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %d", code)
	}
	var made store.Agent
	for _, agent := range agentRows(t, h, user.ID) {
		if agent.Name == "Scout" {
			made = agent
		}
	}
	if made.ID == "" || made.Color != "c2" || made.Color == "c0" {
		t.Fatalf("stored %s %s", made.Name, made.Color)
	}
}

func TestAdvancedColorChoiceAndDefault(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("advanced-color@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.228:9"
	h.login("advanced-color@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	picked := checkedColor(t, body)
	if picked != "c1" {
		t.Fatalf("preselected %s", picked)
	}
	fields := map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {"Scout"}, "title": {"Link checker"},
		"section": {sec.ID}, "model": {model}, "instructions": {"Check the docs."}, "job": {"Check the docs."},
	}
	code, raw, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("default create %d %s", code, raw)
	}
	made := onlyAgent(t, h, user.ID)
	if made.Color != picked {
		t.Fatalf("stored %s preselected %s", made.Color, picked)
	}
	fields["name"] = []string{"Nia"}
	fields["color"] = []string{"c6"}
	code, raw, hdr = h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("chosen create %d %s", code, raw)
	}
	chat := follow(t, h, ip, code, hdr)
	if !strings.Contains(chat, `class="av c6"`) || strings.Contains(chat, `style="`) {
		t.Fatal("chosen color is not on the chat page")
	}
	var nia store.Agent
	for _, agent := range agentRows(t, h, user.ID) {
		if agent.Name == "Nia" {
			nia = agent
		}
	}
	if nia.Color != "c6" {
		t.Fatalf("chosen stored %s", nia.Color)
	}
}

func TestEditColorUpdatesChatBubbles(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("edit-color@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.229:9"
	h.login("edit-color@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Docs helper", Color: "c1", Model: model, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	th, err := h.st.DMThread(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "user", user.ID, "Please check the docs.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "agent", agent.ID, "I found two broken links."); err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Scout"}, "title": {"Docs helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Check the docs."}, "color": {"c6"},
	}
	code, raw, hdr := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/agents/"+agent.ID {
		t.Fatalf("edit %d %s %s", code, hdr.Get("Location"), raw)
	}
	chat := follow(t, h, ip, code, hdr)
	if strings.Contains(chat, "Edit agent") || strings.Contains(chat, colorAdvancedHint) {
		t.Fatal("save stayed on the edit form")
	}
	if !strings.Contains(chat, `class="m-user c6" id="m-`+old.ID+`"`) {
		t.Fatal("existing human bubble did not take the new color")
	}
	if strings.Contains(chat, `class="m-user c1"`) || strings.Contains(chat, `style="`) {
		t.Fatal("old color or inline style remained")
	}
	if strings.Count(chat, `class="av c6"`) < 3 {
		t.Fatal("sidebar, header, or message avatar missed the color")
	}
	fresh, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "user", user.ID, "Please fix the annual billing link too.")
	if err != nil {
		t.Fatal(err)
	}
	code, chat, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(chat, `class="m-user c6" id="m-`+fresh.ID+`"`) || strings.Count(chat, `class="m-user c6"`) != 2 {
		t.Fatal("new human bubble missed the color")
	}
	for _, panel := range []string{"agent", "machine"} {
		code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel="+panel, "", ip, nil)
		if code != 200 || !strings.Contains(body, `id="agent-panel"`) || !strings.Contains(body, `class="av c6 lg`) {
			t.Fatalf("%s panel missed the color", panel)
		}
	}
	got, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil || got.Color != "c6" {
		t.Fatalf("stored %s %v", got.Color, err)
	}
}

func TestColorRejectedOnWrite(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("reject-color@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.230:9"
	h.login("reject-color@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	base := map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {"Scout"}, "title": {"Link checker"},
		"section": {sec.ID}, "model": {model}, "instructions": {"Check the docs."},
	}
	for _, bad := range []string{"", "c0", "#ff00aa", "nope"} {
		fields := map[string][]string{}
		for k, v := range base {
			fields[k] = v
		}
		fields["color"] = []string{bad}
		fields["name"] = []string{"Scout"}
		code, body, _ := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
		if code != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), colorRejectedCopy) {
			t.Fatalf("create %q -> %d", bad, code)
		}
	}
	if rows := agentRows(t, h, user.ID); len(rows) != 0 {
		t.Fatalf("bad create wrote %d agents", len(rows))
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Docs helper", Color: "c1", Model: model, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "c0", "#ff00aa", "c10"} {
		fields := map[string][]string{
			"form": {"agent"}, "name": {"Scout"}, "title": {"Renamed"}, "section": {sec.ID},
			"model": {model}, "instructions": {"Check the docs."}, "color": {bad},
		}
		code, body, _ := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
		if code != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), colorRejectedCopy) {
			t.Fatalf("edit %q -> %d", bad, code)
		}
		got, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
		if err != nil || got.Color != "c1" || got.Title != "Docs helper" {
			t.Fatalf("edit %q wrote %+v %v", bad, got, err)
		}
	}
}

func TestColorPickerAccessible(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("picker-a11y@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.231:9"
	h.login("picker-a11y@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	code, advanced, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	assertPicker(t, advanced, "c1", true)
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Docs helper", Color: "c4", Model: model, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, edit, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	assertPicker(t, edit, "c4", false)
}

func assertPicker(t *testing.T, body, selected string, hint bool) {
	t.Helper()
	if strings.Count(body, `name="color"`) != 9 {
		t.Fatalf("radios %d", strings.Count(body, `name="color"`))
	}
	if strings.Contains(body, `name="color" tabindex="-1"`) || !strings.Contains(body, `<legend>Color</legend>`) {
		t.Fatal("picker is not a labelled radio group")
	}
	for _, c := range agentPalette() {
		if !strings.Contains(body, `<span class="nm">`+c.Name+`</span>`) {
			t.Fatalf("missing visible name %s", c.Name)
		}
		if !strings.Contains(body, `aria-label="Color: `+c.Name+`"`) {
			t.Fatalf("missing accessible name %s", c.Name)
		}
		if !strings.Contains(body, `type="radio" name="color" value="`+c.Key+`"`) {
			t.Fatalf("missing radio %s", c.Key)
		}
	}
	if checkedColor(t, body) != selected {
		t.Fatalf("checked %s want %s", checkedColor(t, body), selected)
	}
	if hint != strings.Contains(body, colorAdvancedHint) {
		t.Fatalf("hint present %v", !hint)
	}
}

func TestColorAuthzAndCSRF(t *testing.T) {
	h := newHarness(t)
	owner, secret := h.account("owner-color@example.com", "Owner", "correct-horse-1")
	ip := "203.0.113.232:9"
	h.login("owner-color@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	agent, _, err := h.st.CreateAgent(context.Background(), owner.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Docs helper", Color: "c1", Model: model, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/edit", "color=c4&csrf=nope&form=agent", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d %s", code, body)
	}
	got, err := h.st.GetAgent(context.Background(), owner.ID, agent.ID)
	if err != nil || got.Color != "c1" {
		t.Fatalf("csrf wrote %s %v", got.Color, err)
	}
	_, otherSecret := h.account("b-color@example.com", "User B", "correct-horse-1")
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	bip := "203.0.113.233:9"
	h.login("b-color@example.com", "correct-horse-1", otherSecret, bip, false)
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Scout"}, "title": {"Stolen"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Check the docs."}, "color": {"c4"},
	}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", bip, fields, nil)
	if code != http.StatusNotFound {
		t.Fatalf("authz %d %s", code, body)
	}
	got, err = h.st.GetAgent(context.Background(), owner.ID, agent.ID)
	if err != nil || got.Color != "c1" || got.Title != "Docs helper" {
		t.Fatalf("b wrote %+v %v", got, err)
	}
}

var checkedColorRE = regexp.MustCompile(`name="color" value="(c[1-9])" checked`)

func checkedColor(t *testing.T, body string) string {
	t.Helper()
	m := checkedColorRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no checked color")
	}
	return m[1]
}
