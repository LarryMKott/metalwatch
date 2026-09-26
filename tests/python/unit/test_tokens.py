# -*- coding: utf-8 -*-
"""单元测试 · API 开放令牌（W11）：创建只回一次明文、列表、吊销。"""
import uuid

import pytest

from mw import client, db

UNIQUE = lambda: uuid.uuid4().hex[:10]  # noqa: E731


def test_token_lifecycle(admin):
    """正向生命周期：创建（明文仅此一次）→ 列表可见（无明文）→ 吊销。"""
    name = "py-tok-" + UNIQUE()
    status, created = admin("POST", "/api/v1/api-tokens",
                            body={"name": name}, expect=(200, 201))
    assert created["token"].startswith("mwo_")
    try:
        _, listed = admin("GET", "/api/v1/api-tokens", expect=200)
        rows = [t for t in listed["items"] if t["name"] == name]
        assert rows, "列表应包含新令牌"
        assert "token" not in rows[0], "列表不得泄露明文令牌"
        assert rows[0]["state"] == "active"

        admin("DELETE", f"/api/v1/api-tokens/{rows[0]['id']}", expect=(200, 204))
        _, listed = admin("GET", "/api/v1/api-tokens", expect=200)
        rows = [t for t in listed["items"] if t["name"] == name]
        assert rows and rows[0]["state"] == "revoked"
    finally:
        rows = db.query("SELECT id FROM api_token WHERE name = ?", (name,))
        for r in rows:
            db.execute("DELETE FROM api_token WHERE id = ?", (r["id"],))


def test_token_create_requires_auth():
    """边界：无令牌创建开放令牌必须 401。"""
    status, _ = client.call("POST", "/api/v1/api-tokens", body={"name": "x"})
    assert status == 401


def test_token_create_with_scopes_and_expiry(admin):
    """正向：自定义 scopes 与有效期创建，明文只出现一次。"""
    name = "py-tok-scope-" + UNIQUE()
    status, created = admin("POST", "/api/v1/api-tokens", body={
        "name": name, "scopes": "asset:read", "expire_days": 7}, expect=(200, 201))
    assert created["token"].startswith("mwo_")
    try:
        _, listed = admin("GET", "/api/v1/api-tokens", expect=200)
        rows = [t for t in listed["items"] if t["name"] == name]
        assert rows and rows[0]["scopes"] == "asset:read" and rows[0]["expire_at"]
    finally:
        rows = db.query("SELECT id FROM api_token WHERE name = ?", (name,))
        for r in rows:
            db.execute("DELETE FROM api_token WHERE id = ?", (r["id"],))
