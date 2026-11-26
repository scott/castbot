# Slack

Channels the bot has joined. The bot token stays in the vault under `SLACK_CONNECTION`. The agent never sees it. This is a bot token, so the agent does not post as the owner.

Reads: `slack_list_channels`, `slack_read_channel`, `slack_read_thread`. A call returns at most 20 messages. Writes, which ask first: `slack_post` and `slack_reply`. There is no delete, no admin call, no file upload, and no post to a user id. A private channel is available only after the bot is invited.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
