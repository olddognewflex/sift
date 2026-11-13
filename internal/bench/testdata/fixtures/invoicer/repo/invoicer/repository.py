"""SQLite persistence for invoices."""

from __future__ import annotations

import sqlite3
from datetime import date

from invoicer.models import Invoice, Line

SCHEMA = """
CREATE TABLE IF NOT EXISTS invoices (
    number      TEXT PRIMARY KEY,
    customer    TEXT NOT NULL,
    issued      TEXT NOT NULL,
    region      TEXT NOT NULL,
    amount_paid REAL NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS lines (
    invoice     TEXT NOT NULL REFERENCES invoices(number) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    description TEXT NOT NULL,
    quantity    INTEGER NOT NULL,
    price       REAL NOT NULL,
    PRIMARY KEY (invoice, position)
);
"""

_INVOICE_COLUMNS = "number, customer, issued, region, amount_paid"


class InvoiceRepository:
    """Loads and saves invoices with their lines."""

    def __init__(self, path: str) -> None:
        self.conn = sqlite3.connect(path)
        self.conn.execute("PRAGMA foreign_keys = ON")
        self.conn.executescript(SCHEMA)

    def close(self) -> None:
        self.conn.close()

    def save(self, invoice: Invoice) -> None:
        """Insert or replace an invoice and all of its lines."""
        with self.conn:
            self.conn.execute(
                f"INSERT INTO invoices ({_INVOICE_COLUMNS}) VALUES (?, ?, ?, ?, ?) "
                "ON CONFLICT(number) DO UPDATE SET customer = excluded.customer, "
                "issued = excluded.issued, region = excluded.region, amount_paid = excluded.amount_paid",
                (invoice.number, invoice.customer, invoice.issued.isoformat(), invoice.region, invoice.amount_paid),
            )
            self.conn.execute("DELETE FROM lines WHERE invoice = ?", (invoice.number,))
            self.conn.executemany(
                "INSERT INTO lines (invoice, position, description, quantity, price) VALUES (?, ?, ?, ?, ?)",
                [(invoice.number, i, ln.description, ln.quantity, ln.price) for i, ln in enumerate(invoice.lines)],
            )

    def get(self, number: str) -> Invoice | None:
        row = self.conn.execute(f"SELECT {_INVOICE_COLUMNS} FROM invoices WHERE number = ?", (number,)).fetchone()
        return None if row is None else self._invoice(row)

    def list_by_month(self, month: str) -> list[Invoice]:
        """Invoices issued in ``month`` (YYYY-MM), oldest first."""
        rows = self.conn.execute(
            f"SELECT {_INVOICE_COLUMNS} FROM invoices WHERE substr(issued, 1, 7) = ? ORDER BY issued, number",
            (month,),
        ).fetchall()
        return [self._invoice(row) for row in rows]

    def list_all(self) -> list[Invoice]:
        """Every invoice, oldest first."""
        rows = self.conn.execute(f"SELECT {_INVOICE_COLUMNS} FROM invoices ORDER BY issued, number").fetchall()
        return [self._invoice(row) for row in rows]

    def _invoice(self, row: tuple) -> Invoice:
        number, customer, issued, region, amount_paid = row
        lines = [
            Line(description, quantity, price)
            for description, quantity, price in self.conn.execute(
                "SELECT description, quantity, price FROM lines WHERE invoice = ? ORDER BY position", (number,)
            )
        ]
        return Invoice(number, customer, date.fromisoformat(issued), region, lines=lines, amount_paid=amount_paid)
