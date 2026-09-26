import pytest

from analytics import UnsafeQuery, guard_sql
from bitrix24 import build_task_payload, create_task


@pytest.mark.parametrize(
    "sql",
    [
        "SELECT count() FROM postings FINAL",
        "with t as (select 1) select * from t;",
        "SELECT name FROM postings WHERE name = 'drop table'",  # ключевое слово внутри строки — ок
    ],
)
def test_allows_selects(sql):
    assert guard_sql(sql)


@pytest.mark.parametrize(
    "sql",
    [
        "",
        "DROP TABLE postings",
        "SELECT 1; DROP TABLE postings",
        "INSERT INTO postings SELECT * FROM postings",
        "SELECT * FROM postings SETTINGS readonly = 0",
        "ALTER TABLE postings DELETE WHERE 1",
        "WITH x AS (SELECT 1) SELECT * FROM system.users",
    ],
)
def test_blocks_unsafe(sql):
    with pytest.raises(UnsafeQuery):
        guard_sql(sql)


def test_bitrix_payload_and_dry_run(monkeypatch):
    monkeypatch.delenv("BITRIX24_WEBHOOK_URL", raising=False)
    p = build_task_payload("x" * 300, "desc", 2, 7)
    assert len(p["fields"]["TITLE"]) == 255
    assert p["fields"]["RESPONSIBLE_ID"] == 7
    assert "DEADLINE" in p["fields"]
    res = create_task("Пополнить остаток", "Плед заканчивается")
    assert res["dry_run"] is True and res["method"] == "tasks.task.add"
