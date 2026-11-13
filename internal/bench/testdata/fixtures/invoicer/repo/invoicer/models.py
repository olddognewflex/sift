"""Invoices and their lines."""

from __future__ import annotations

import math
from dataclasses import dataclass
from datetime import date

from invoicer import tax


@dataclass(frozen=True)
class Line:
    """One invoice line. ``price`` is the price of the whole line."""

    description: str
    quantity: int
    price: float

    def __post_init__(self) -> None:
        if not self.description.strip():
            raise ValueError("line description is required")
        if isinstance(self.quantity, bool) or not isinstance(self.quantity, int) or self.quantity <= 0:
            raise ValueError(f"line quantity must be a positive integer, got {self.quantity!r}")
        if not math.isfinite(self.price) or self.price < 0:
            raise ValueError(f"line price must be a finite, non-negative number, got {self.price!r}")


class Invoice:
    """An invoice to one customer, taxed at the rate of its region."""

    def __init__(
        self,
        number: str,
        customer: str,
        issued: date,
        region: str,
        lines: list[Line] = [],
        amount_paid: float = 0.0,
    ) -> None:
        self.number = number
        self.customer = customer
        self.issued = issued
        self.region = region
        self.lines = lines
        self.amount_paid = amount_paid

    def add_line(self, line: Line) -> None:
        self.lines.append(line)

    def subtotal(self) -> float:
        return sum(line.price for line in self.lines)

    def tax(self) -> float:
        return tax.tax_for(self)

    def total(self) -> float:
        return self.subtotal() + self.tax()

    def record_payment(self, amount: float) -> None:
        if amount <= 0:
            raise ValueError(f"payment must be positive, got {amount}")
        self.amount_paid += amount

    def balance_due(self) -> float:
        return self.total() - self.amount_paid

    def is_paid(self) -> bool:
        return self.amount_paid == self.total()

    def __repr__(self) -> str:
        return f"Invoice({self.number!r}, {self.customer!r}, {self.issued.isoformat()}, {len(self.lines)} lines)"
