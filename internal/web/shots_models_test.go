//go:build shots

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func shootModelSetup(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	const key = "fixture-shot-or"
	ctx := openModelShot(t, browser, "models-setup-"+name+"@example.com", "/new-agent", width, height, key, "openrouter", func(t *testing.T, h *harness, user store.User) string {
		raw, err := json.Marshal(draftDoc{Editing: true, Card: &setupState{
			Name: "Scout", Title: "Helper", Job: "Check the docs", Instructions: "Check the docs.",
			Model: firstModel(t), Size: createSize, Idle: createIdle,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.SaveCreateDraft(context.Background(), user.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
		return ""
	})
	assertProviderOnly(t, ctx, "openrouter")
	assertNoModelKey(t, ctx, key)
	var shown bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText||'').indexOf('Server key')>=0 && (document.body.innerText||'').indexOf('Editing')>=0`), &shown)); err != nil || !shown {
		t.Fatal("setup card did not show the picker")
	}
	shotPage(t, ctx, filepath.Join(dir, "setup-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootModelAdvanced(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	const key = "fixture-shot-adv"
	ctx := openModelShot(t, browser, "models-adv-"+name+"@example.com", "/new-agent?advanced=1", width, height, key, "openrouter", nil)
	assertProviderOnly(t, ctx, "openrouter")
	assertNoModelKey(t, ctx, key)
	assertNoFreeformModel(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "advanced-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootModelEdit(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	const key = "fixture-shot-edit"
	ctx := openModelShot(t, browser, "models-edit-"+name+"@example.com", "", width, height, key, "openrouter", func(t *testing.T, h *harness, user store.User) string {
		agent := seedAgent(t, h.st, user.ID, "Scout")
		model := firstModel(t)
		if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{
			SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Scout\nbrain:\n  model: "+model+"\n", "Check the docs.\n"); err != nil {
			t.Fatal(err)
		}
		return "/agents/" + agent.ID + "/edit"
	})
	assertProviderOnly(t, ctx, "openrouter")
	assertNoModelKey(t, ctx, key)
	var label bool
	want := modelLabel(firstModel(t))
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText||'').indexOf('`+want+`')>=0`), &label)); err != nil || !label {
		t.Fatal("edit form did not show the model")
	}
	shotPage(t, ctx, filepath.Join(dir, "edit-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootModelDO(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	const key = "fixture-shot-do"
	ctx := openModelShot(t, browser, "models-do-"+name+"@example.com", "/new-agent?advanced=1", width, height, key, "digitalocean", nil)
	assertProviderOnly(t, ctx, "digitalocean")
	assertNoModelKey(t, ctx, key)
	var models string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(selectValuesJS("model")), &models)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(models, "digitalocean/deepseek-v4.1-flash") || !strings.Contains(models, "digitalocean/deepseek-3.2") || strings.Contains(models, "llama3.3") || strings.Contains(models, "deepseek-v3.2") {
		t.Fatalf("do models %s", models)
	}
	shotPage(t, ctx, filepath.Join(dir, "do-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootModelDelisted(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	const key = "fixture-shot-old"
	const kept = "digitalocean/llama3.3-70b-instruct"
	ctx := openModelShot(t, browser, "models-old-"+name+"@example.com", "", width, height, key, "digitalocean", func(t *testing.T, h *harness, user store.User) string {
		agent := seedAgent(t, h.st, user.ID, "Scout")
		if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{
			SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: kept,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Scout\nbrain:\n  model: "+kept+"\n", "Check the docs.\n"); err != nil {
			t.Fatal(err)
		}
		return "/agents/" + agent.ID + "/edit"
	})
	assertProviderOnly(t, ctx, "digitalocean")
	assertNoModelKey(t, ctx, key)
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.querySelector('select[name="model"]')||{}).value==='`+kept+`' && (document.body.innerText||'').indexOf('no longer listed')>=0`), &ok)); err != nil || !ok {
		t.Fatal("delisted model was not kept")
	}
	shotPage(t, ctx, filepath.Join(dir, "delisted-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootModelNoKey(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	ctx := openModelShot(t, browser, "models-nokey-"+name+"@example.com", "/new-agent?advanced=1", width, height, "", "", nil)
	var dis bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`!!(document.querySelector('select[name="provider"]')||{}).disabled && !!(document.querySelector('select[name="model"]')||{}).disabled`), &dis)); err != nil || !dis {
		t.Fatal("no-key selects stayed enabled")
	}
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.body.innerText||''`), &text)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Add a model key first") || !strings.Contains(text, "Add a key in Secrets") {
		t.Fatal("no-key copy missing")
	}
	shotPage(t, ctx, filepath.Join(dir, "nokey-"+name+".png"))
	assertShotClean(t, ctx)
}

func openModelShot(t *testing.T, browser context.Context, email, path string, width, height int, key, provider string, prep func(*testing.T, *harness, store.User) string) context.Context {
	t.Helper()
	quietProviderKeys(t)
	if provider != "" {
		t.Setenv(castcfg.ProviderKeyEnv(provider), key)
	}
	h := newHarness(t)
	h.srv.ModelCatalog().SetOffline()
	user, _ := h.account(email, "Shot User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	if prep != nil {
		if next := prep(t, h, user); next != "" {
			path = next
		}
	}
	if path == "" {
		t.Fatal("missing page")
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+path, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotVisible(t, `#model-pick`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.getElementById('model-pick');if(el)el.scrollIntoView({block:'center'});})()`), nil)); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func quietProviderKeys(t *testing.T) {
	t.Helper()
	for _, p := range castcfg.KeyedProviders() {
		t.Setenv(castcfg.ProviderKeyEnv(p.Key), "")
	}
	t.Setenv("OLLAMA_BASE_URL", "")
}

func assertProviderOnly(t *testing.T, ctx context.Context, provider string) {
	t.Helper()
	var got string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(selectValuesJS("provider")), &got)); err != nil {
		t.Fatal(err)
	}
	if got != provider {
		t.Fatalf("providers %q", got)
	}
}

func assertNoModelKey(t *testing.T, ctx context.Context, key string) {
	t.Helper()
	var html string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if key != "" && strings.Contains(html, key) {
		t.Fatal("page contained the model key")
	}
}

func assertNoFreeformModel(t *testing.T, ctx context.Context) {
	t.Helper()
	var n int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelectorAll('input[name="model"]').length`), &n)); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("freeform model inputs %d", n)
	}
}

func selectValuesJS(name string) string {
	return `(function(){var s=document.querySelector('select[name="` + name + `"]');if(!s)return '';var o=[];for(var i=0;i<s.options.length;i++){if(s.options[i].value)o.push(s.options[i].value);}return o.join('\n');})()`
}
