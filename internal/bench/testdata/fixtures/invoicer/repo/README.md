# invoicer

A small invoicing tool for a consultancy: import invoices from CSV, record
payments, and print monthly summaries. Data lives in a SQLite file.

```sh
python -m invoicer.cli --db invoices.db import march.csv
python -m invoicer.cli pay INV-1042 1190.00
python -m invoicer.cli summary --month 2024-03
python -m invoicer.cli show INV-1042
python -m invoicer.cli top --limit 5
```

The import CSV has one row per invoice line, with the columns `number`,
`customer`, `issued` (YYYY-MM-DD), `region`, `description`, `quantity` and
`price` (the price of the whole line).

Run the tests with `pytest`.
