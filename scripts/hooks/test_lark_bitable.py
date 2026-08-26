#!/usr/bin/env python3
"""Tests for the token cache in lark_bitable.py.

    python3 scripts/hooks/test_lark_bitable.py      # or: make test-hooks

Stdlib only, and no network: every test replaces the one function that would
make an HTTP call. The cache is the part worth testing — it is an optimisation
holding a live credential, so its failure modes (stale, corrupt, rejected) all
have to degrade into "fetch a fresh one" rather than into a broken hook.
"""

import importlib.util
import json
import os
import stat
import tempfile
import time
import unittest
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "lark_bitable", Path(__file__).with_name("lark_bitable.py"))
lark = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(lark)


class CacheTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = Path(self.dir.name) / "token.json"
        self.addCleanup(self.dir.cleanup)

    def test_roundtrip(self):
        lark.write_cached_token(self.path, "t-abc", 3600)
        self.assertEqual(lark.read_cached_token(self.path), "t-abc")

    def test_file_is_a_credential(self):
        """0600: /tmp is world-readable, and this file grants API access."""
        lark.write_cached_token(self.path, "t-abc", 3600)
        mode = stat.S_IMODE(self.path.stat().st_mode)
        self.assertEqual(mode, 0o600, f"cache mode is {oct(mode)}")

    def test_margin_is_applied(self):
        """The stored expiry is short of the server's, so a token cannot go
        stale between building a request and its arrival."""
        lark.write_cached_token(self.path, "t-abc", 3600)
        data = json.loads(self.path.read_text())
        slack = data["expires_at"] - time.time()
        self.assertLess(slack, 3600 - lark.TOKEN_MARGIN + 5)
        self.assertGreater(slack, 3600 - lark.TOKEN_MARGIN - 5)

    def test_expired_reads_as_miss(self):
        self.path.write_text(json.dumps({"token": "t-old", "expires_at": time.time() - 1}))
        self.assertEqual(lark.read_cached_token(self.path), "")

    def test_corrupt_or_absent_reads_as_miss(self):
        """Every way the file can be wrong resolves to a fetch, not a crash:
        a cache must never be able to break the caller."""
        self.assertEqual(lark.read_cached_token(self.path), "", "missing file")
        for bad in ['{"token": "t", ', 'null', '[]', '{}', '{"token": ""}',
                    '{"token": "t", "expires_at": "soon"}', '']:
            self.path.write_text(bad)
            self.assertEqual(lark.read_cached_token(self.path), "", f"corrupt: {bad!r}")

    def test_short_lived_token_is_not_cached(self):
        """`expire` is REMAINING life, so a nearly-dead token comes back with a
        tiny value. Caching it would serve an expired credential."""
        calls = []

        def fake_fetch(app_id, app_secret):
            calls.append(1)
            return "t-almost-dead", lark.TOKEN_MARGIN - 1

        with patched(lark, fetch_token=fake_fetch, TOKEN_CACHE=str(self.path)):
            self.assertEqual(lark.tenant_token("id", "secret"), "t-almost-dead")
            self.assertEqual(lark.tenant_token("id", "secret"), "t-almost-dead")
        self.assertEqual(len(calls), 2, "a short-lived token must not be cached")
        self.assertFalse(self.path.exists())


class TenantTokenTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = Path(self.dir.name) / "token.json"
        self.addCleanup(self.dir.cleanup)
        self.calls = []

    def fake_fetch(self, app_id, app_secret):
        self.calls.append(app_id)
        return f"t-{len(self.calls)}", 3600

    def test_second_call_hits_the_cache(self):
        with patched(lark, fetch_token=self.fake_fetch, TOKEN_CACHE=str(self.path)):
            first = lark.tenant_token("id", "secret")
            second = lark.tenant_token("id", "secret")
        self.assertEqual(first, second)
        self.assertEqual(len(self.calls), 1, "the second call should not hit the network")

    def test_refresh_bypasses_and_replaces(self):
        with patched(lark, fetch_token=self.fake_fetch, TOKEN_CACHE=str(self.path)):
            lark.tenant_token("id", "secret")
            refreshed = lark.tenant_token("id", "secret", refresh=True)
            after = lark.tenant_token("id", "secret")
        self.assertEqual(len(self.calls), 2)
        self.assertEqual(refreshed, "t-2")
        self.assertEqual(after, "t-2", "refresh must replace the cache, not bypass it once")

    def test_disabled_never_writes(self):
        with patched(lark, fetch_token=self.fake_fetch, TOKEN_CACHE=str(self.path),
                     NO_TOKEN_CACHE=True):
            lark.tenant_token("id", "secret")
            lark.tenant_token("id", "secret")
        self.assertEqual(len(self.calls), 2)
        self.assertFalse(self.path.exists())

    def test_cache_key_does_not_leak_the_app_id(self):
        """/tmp is world-listable; the filename must not advertise the app."""
        with patched(lark, TOKEN_CACHE=""):
            name = lark.cache_path("cli_aa0377acf1b8dbfb").name
        self.assertNotIn("cli_", name)
        self.assertNotIn("aa0377", name)


class BadTokenTest(unittest.TestCase):
    def test_classification(self):
        self.assertTrue(lark.LarkError("x", http=401).is_bad_token())
        self.assertTrue(lark.LarkError("x", code=99991663).is_bad_token())
        self.assertFalse(lark.LarkError("x", code=99991400).is_bad_token(), "rate limit")
        self.assertFalse(lark.LarkError("boom").is_bad_token(), "transport failure")

    def test_rejected_cached_token_is_refetched_once(self):
        """The failure this exists to prevent: a stale cache turning "one slow
        call per email" into "every email fails until someone clears /tmp"."""
        tokens, attempts = [], []

        def fake_tenant(app_id, app_secret, refresh=False):
            tokens.append(refresh)
            return "t-fresh" if refresh else "t-stale"

        def fake_create(token, fields):
            attempts.append(token)
            if token == "t-stale":
                raise lark.LarkError("lark code 99991663", code=99991663)
            return "recXYZ"

        with patched(lark, tenant_token=fake_tenant, create_record=fake_create):
            rc = lark.main(["prog", "<id@x>", "subj", "body", "a@b.com", "new_thread"])

        self.assertEqual(rc, 0)
        self.assertEqual(attempts, ["t-stale", "t-fresh"], "should retry once, refreshed")
        self.assertEqual(tokens, [False, True])

    def test_other_errors_are_not_retried(self):
        """A schema or permission error is not fixed by a new token, and
        retrying would double every failed write."""
        attempts = []

        def fake_create(token, fields):
            attempts.append(token)
            raise lark.LarkError("lark code 1254045: FieldNameNotFound", code=1254045)

        with patched(lark, tenant_token=lambda *a, **k: "t-ok", create_record=fake_create):
            rc = lark.main(["prog", "<id@x>", "subj", "body", "a@b.com", "new_thread"])

        self.assertEqual(rc, 1)
        self.assertEqual(len(attempts), 1)


class patched:
    """Swap module attributes for the duration of a with-block."""

    def __init__(self, module, **attrs):
        self.module = module
        self.attrs = attrs
        self.saved = {}

    def __enter__(self):
        for k, v in self.attrs.items():
            self.saved[k] = getattr(self.module, k)
            setattr(self.module, k, v)
        return self.module

    def __exit__(self, *exc):
        for k, v in self.saved.items():
            setattr(self.module, k, v)
        return False


if __name__ == "__main__":
    os.environ.setdefault("APP_ID", "test-app")
    os.environ.setdefault("APP_SECRET", "test-secret")
    unittest.main(verbosity=2)
