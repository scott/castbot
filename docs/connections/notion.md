# Notion

Pages shared with an integration. The token stays in the vault under `NOTION_CONNECTION`. The agent never sees it.

Every call goes to `api.notion.com` with `Notion-Version: 2022-06-28`. Reads: `notion_search`, `notion_get_page`, `notion_get_blocks`. Writes, which ask first: `notion_create_page` and `notion_append`. A new page is a child of a page, not a database. There is no delete, no permission change, and no database schema edit. The integration only sees pages the owner shared with it.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
