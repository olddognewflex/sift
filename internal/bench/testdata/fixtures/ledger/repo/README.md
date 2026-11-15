# ledger

A small in-memory ledger service: accounts with integer balances in minor
units (cents), transfers between accounts, and per-account statements.

```sh
go run ./cmd/ledgerd -addr :8080
```

## API

| Method | Path | |
|---|---|---|
| `GET` | `/accounts?offset=&limit=` | Accounts, sorted by ID, paged |
| `POST` | `/accounts` | Open an account: `{"id", "owner", "currency"}` |
| `GET` | `/accounts/{id}` | One account |
| `DELETE` | `/accounts/{id}` | Close an account (balance must be zero) |
| `GET` | `/accounts/{id}/statement` | The account's entries, oldest first |
| `POST` | `/transfers` | `{"from", "to", "amount", "ref"}` |

State lives in memory and is lost on restart. `-seed` loads opening
accounts from a JSON array of `{"id", "owner", "currency", "balance"}`.
