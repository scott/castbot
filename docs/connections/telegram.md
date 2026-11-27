# Telegram

One chat the owner named. The bot token and the chat id stay in the vault under `TELEGRAM_CONNECTION`. The agent never sees the token and cannot pick a different chat.

Reads: `telegram_read` for recent messages in that chat. A read does not delete messages. Writes, which ask first: `telegram_send` to that chat only. There is no other group, and no admin method.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
