"""Sales tax by region."""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from invoicer.models import Invoice

# Standard rates by region code.
RATES: dict[str, float] = {
    "DE": 0.19,
    "FR": 0.20,
    "IE": 0.23,
    "NL": 0.21,
    "UK": 0.20,
}


class UnknownRegion(ValueError):
    """Raised for a region with no configured tax rate."""


def rate_for(region: str) -> float:
    """The standard tax rate for a region code, case-insensitive."""
    try:
        return RATES[region.upper()]
    except KeyError:
        raise UnknownRegion(f"no tax rate for region {region!r}") from None


def tax_for(invoice: Invoice) -> float:
    """Tax due on an invoice's subtotal, rounded to cents."""
    return round(invoice.subtotal() * rate_for(invoice.region), 2)
