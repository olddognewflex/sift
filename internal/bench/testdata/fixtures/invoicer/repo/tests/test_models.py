from datetime import date

import pytest

from invoicer.models import Invoice, Line


def test_line_rejects_non_positive_quantity():
    with pytest.raises(ValueError):
        Line("Consulting", 0, 100.0)


def test_line_rejects_negative_price():
    with pytest.raises(ValueError):
        Line("Discount", 1, -10.0)


def test_total_includes_tax():
    inv = Invoice("INV-1", "Acme GmbH", date(2024, 3, 4), "DE", lines=[Line("Consulting", 2, 200.0)])
    assert inv.subtotal() == 200.0
    assert inv.tax() == 38.0
    assert inv.total() == 238.0


def test_payment_settles_invoice():
    inv = Invoice("INV-2", "Acme GmbH", date(2024, 3, 4), "DE", lines=[Line("Support", 1, 100.0)])
    assert not inv.is_paid()
    inv.record_payment(119.0)
    assert inv.is_paid()
    assert inv.balance_due() == 0


def test_payment_must_be_positive():
    inv = Invoice("INV-3", "Acme GmbH", date(2024, 3, 4), "DE", lines=[Line("Support", 1, 100.0)])
    with pytest.raises(ValueError):
        inv.record_payment(0)
