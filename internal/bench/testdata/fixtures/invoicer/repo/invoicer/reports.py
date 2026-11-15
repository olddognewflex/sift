"""Reports over invoices."""

from __future__ import annotations

import sqlite3
from dataclasses import dataclass

from invoicer.models import Invoice


@dataclass(frozen=True)
class Summary:
    count: int
    unpaid: int
    invoiced: float
    average: float


def line_rows(invoice: Invoice) -> list[tuple[str, int, float, float]]:
    """Rows for an invoice's detail table: description, quantity, unit
    price and line price."""
    return [
        (line.description, line.quantity, round(line.price / line.quantity, 2), line.price)
        for line in invoice.lines
    ]


def summarize(invoices: list[Invoice]) -> Summary:
    """Totals over a set of invoices: how many, how many are unpaid, the
    amount invoiced including tax, and the average invoice."""
    invoiced = sum(inv.total() for inv in invoices)
    return Summary(
        count=len(invoices),
        unpaid=sum(1 for inv in invoices if not inv.is_paid()),
        invoiced=round(invoiced, 2),
        average=round(invoiced / len(invoices), 2),
    )


def format_summary(label: str, s: Summary) -> str:
    return "\n".join(
        [
            f"{label}",
            f"  invoices  {s.count:>10}",
            f"  unpaid    {s.unpaid:>10}",
            f"  invoiced  {s.invoiced:>10.2f}",
            f"  average   {s.average:>10.2f}",
        ]
    )


def top_customers(conn: sqlite3.Connection, limit: int = 10) -> list[tuple[str, float]]:
    """Customers by revenue before tax, highest first."""
    rows = conn.execute(
        "SELECT i.customer, SUM(l.price) AS revenue "
        "FROM invoices i JOIN lines l ON l.invoice = i.number "
        "GROUP BY i.customer ORDER BY revenue DESC LIMIT ?",
        (limit,),
    ).fetchall()
    return [(customer, round(revenue, 2)) for customer, revenue in rows]
