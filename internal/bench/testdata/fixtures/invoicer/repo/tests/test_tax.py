from datetime import date

import pytest

from invoicer.models import Invoice, Line
from invoicer.tax import UnknownRegion, rate_for, tax_for


def test_rate_lookup_is_case_insensitive():
    assert rate_for("fr") == rate_for("FR") == 0.20


def test_unknown_region():
    with pytest.raises(UnknownRegion):
        rate_for("ZZ")


def test_tax_is_rounded_to_cents():
    inv = Invoice("INV-9", "Studio BV", date(2024, 5, 1), "NL", lines=[Line("Design", 3, 33.33)])
    assert tax_for(inv) == 7.0
