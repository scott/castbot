# Web

Search the public web and read one public page. The search key stays in the vault under `WEB_CONNECTION`. The agent never sees it.

`web_search` is one GET to `api.search.brave.com` with the header `X-Subscription-Token`. The count is at most 10. The result is titles, URLs, and snippets.

`web_read` fetches one https URL. It is the only Web tool whose argument is a URL. The host refuses userinfo, an IP address, a port other than 443, and a name that resolves only to a private, link-local, multicast, or carrier-grade address. A redirect is checked the same way, up to three. The page comes back as text, capped around 200 KB. The search key is not sent.

There is no POST, no stored cookie, and no browser on the control plane.

Reads are always-allow until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
