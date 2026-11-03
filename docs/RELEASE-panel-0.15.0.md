# WhiteDNS Server Management 0.15.0

Publication uses the single manually triggered build-and-release workflow.
Installs and in-panel updates now come from the `driftcodes/Soar-Server`
releases.

- Per-account tunnel domains. On servers with several tunnel domains, each
  account can use all of them (the default, including domains added later) or a
  chosen subset. Set it when creating or editing an account in the panel, or
  from the Telegram bot's **🌐 Domains** button.
- The limit is enforced by the server for Soar, Cotten and MasterDNS: an account
  is not routed on other domains, so editing a profile's domain by hand does not
  bypass it. Subscriptions and direct links contain only the allowed domains.
- Keys, usage and subscription URLs are unchanged by domain edits; refresh the
  subscription in the app afterwards.
- Removing a tunnel domain, from the panel or Telegram, also removes it from
  restricted accounts. A removal that would leave a restricted account with no
  domain is refused and names the account.
- Existing accounts keep access to every domain. Backups carry the per-account
  domain lists.

The API field is `tunnel_zones` on `POST/PUT /api/clients`; see
[PANEL.en.md](https://github.com/driftcodes/Soar-Server/blob/main/docs/PANEL.en.md)
and [DOMAINS.fa.md](https://github.com/driftcodes/Soar-Server/blob/main/docs/DOMAINS.fa.md).
