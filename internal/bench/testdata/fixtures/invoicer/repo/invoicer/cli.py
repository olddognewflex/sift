"""Command-line interface: ``python -m invoicer.cli <command> ...``."""

from __future__ import annotations

import argparse
import csv
import math
import re
import sys
from datetime import date

from invoicer import reports
from invoicer.models import Invoice, Line
from invoicer.repository import InvoiceRepository
from invoicer.tax import rate_for

_MONTH = re.compile(r"^\d{4}-(0[1-9]|1[0-2])$")


def month_arg(value: str) -> str:
    if not _MONTH.match(value):
        raise argparse.ArgumentTypeError(f"expected YYYY-MM, got {value!r}")
    return value


def amount_arg(value: str) -> float:
    try:
        amount = float(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"not a number: {value!r}") from None
    if not math.isfinite(amount) or amount <= 0:
        raise argparse.ArgumentTypeError(f"amount must be a positive number, got {value}")
    return amount


def cmd_import(repo: InvoiceRepository, args: argparse.Namespace) -> int:
    invoices: dict[str, Invoice] = {}
    with open(args.csv, newline="") as f:
        for row in csv.DictReader(f):
            number = row["number"]
            if number not in invoices:
                rate_for(row["region"])
                invoices[number] = Invoice(number, row["customer"], date.fromisoformat(row["issued"]), row["region"])
            invoices[number].add_line(Line(row["description"], int(row["quantity"]), float(row["price"])))
    existing = [number for number in invoices if repo.get(number) is not None]
    if existing:
        print(f"already imported: {', '.join(existing)}", file=sys.stderr)
        return 1
    for invoice in invoices.values():
        repo.save(invoice)
    print(f"imported {len(invoices)} invoices")
    return 0


def cmd_pay(repo: InvoiceRepository, args: argparse.Namespace) -> int:
    invoice = repo.get(args.number)
    if invoice is None:
        print(f"no invoice {args.number}", file=sys.stderr)
        return 1
    invoice.record_payment(args.amount)
    repo.save(invoice)
    state = "paid" if invoice.is_paid() else f"{invoice.balance_due():.2f} due"
    print(f"{invoice.number}: {state}")
    return 0


def cmd_summary(repo: InvoiceRepository, args: argparse.Namespace) -> int:
    if args.month:
        invoices, label = repo.list_by_month(args.month), args.month
    else:
        invoices, label = repo.list_all(), "all time"
    print(reports.format_summary(label, reports.summarize(invoices)))
    return 0


def cmd_show(repo: InvoiceRepository, args: argparse.Namespace) -> int:
    invoice = repo.get(args.number)
    if invoice is None:
        print(f"no invoice {args.number}", file=sys.stderr)
        return 1
    print(f"{invoice.number}  {invoice.customer}  {invoice.issued.isoformat()}")
    for description, quantity, unit, price in reports.line_rows(invoice):
        print(f"  {description:<40} {quantity:>5} x {unit:>10.2f} = {price:>10.2f}")
    print(f"  {'tax':<40} {invoice.tax():>31.2f}")
    print(f"  {'total':<40} {invoice.total():>31.2f}")
    return 0


def cmd_top(repo: InvoiceRepository, args: argparse.Namespace) -> int:
    for customer, revenue in reports.top_customers(repo.conn, args.limit):
        print(f"{customer:<40} {revenue:>12.2f}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="invoicer")
    parser.add_argument("--db", default="invoices.db", help="SQLite database file")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("import", help="import invoices from a CSV file")
    p.add_argument("csv")
    p.set_defaults(func=cmd_import)

    p = sub.add_parser("pay", help="record a payment against an invoice")
    p.add_argument("number")
    p.add_argument("amount", type=amount_arg)
    p.set_defaults(func=cmd_pay)

    p = sub.add_parser("summary", help="summarize invoices, optionally for one month")
    p.add_argument("--month", type=month_arg, help="YYYY-MM")
    p.set_defaults(func=cmd_summary)

    p = sub.add_parser("show", help="print one invoice")
    p.add_argument("number")
    p.set_defaults(func=cmd_show)

    p = sub.add_parser("top", help="customers by revenue")
    p.add_argument("--limit", type=int, default=10)
    p.set_defaults(func=cmd_top)

    args = parser.parse_args(argv)
    repo = InvoiceRepository(args.db)
    try:
        return args.func(repo, args)
    finally:
        repo.close()


if __name__ == "__main__":
    sys.exit(main())
