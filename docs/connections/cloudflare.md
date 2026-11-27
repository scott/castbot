# Cloudflare

Zones this token can see. The token stays in the vault under `CLOUDFLARE_CONNECTION`. The agent never sees it. This connection does not create machines.

Reads: `cloudflare_list_zones`, `cloudflare_list_dns`, `cloudflare_zone_summary`. Writes, which ask first: `cloudflare_upsert_dns` and `cloudflare_purge_cache`. A DNS write is an A, AAAA, CNAME, or TXT record. A purge names one https URL and does not clear the whole zone. There is no member API, no token create, no billing, and no Workers deploy.

Reads are always-allow until you change them. Writes stay ask until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
