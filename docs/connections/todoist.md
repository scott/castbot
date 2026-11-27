# Todoist

Tasks on this account. The token stays in the vault under `TODOIST_CONNECTION`. The agent never sees it.

Every call uses `api.todoist.com` and REST `v2`. Reads: `todoist_list_tasks`, `todoist_get_task`. Writes, which ask first: `todoist_create_task`, `todoist_complete`, and `todoist_comment`. A new task has text only. A comment is on a task. There is no project delete and no sharing.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
