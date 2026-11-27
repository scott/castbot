# Stripe

Whether money moved. The key stays in the vault under `STRIPE_CONNECTION`. The agent never sees it. Paste a restricted read key.

Reads: `stripe_balance`, `stripe_list_charges`, `stripe_get_customer`, `stripe_list_payment_intents`. The key is sent as the HTTP basic user. There is no create, refund, payout, transfer, or customer write.

Reads are always-allow until you change them. Disconnect keeps the vault row and turns the connection off for every agent.
